package repository

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"slices"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const groupAutoRateEqualityEpsilon = 1e-9

type groupAutoRateRepository struct {
	client *dbent.Client
	sql    sqlExecutor
}

// NewGroupAutoRateRepository creates a repository for automatic group rate synchronization.
func NewGroupAutoRateRepository(client *dbent.Client, sqlDB *sql.DB) service.GroupAutoRateRepository {
	return newGroupAutoRateRepositoryWithSQL(client, sqlDB)
}

func newGroupAutoRateRepositoryWithSQL(client *dbent.Client, sqlq sqlExecutor) *groupAutoRateRepository {
	return &groupAutoRateRepository{client: client, sql: sqlq}
}

func (r *groupAutoRateRepository) queryExecutor(ctx context.Context) sqlExecutor {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	if r.sql != nil {
		return r.sql
	}
	if r.client != nil {
		return r.client
	}
	return nil
}

func (r *groupAutoRateRepository) GetGroupAutoRateEligibleSummary(ctx context.Context, groupID int64, requireOAuthOnly bool) (*service.GroupAutoRateEligibleSummary, error) {
	if groupID <= 0 {
		return &service.GroupAutoRateEligibleSummary{}, nil
	}
	exec := r.queryExecutor(ctx)
	if exec == nil {
		return &service.GroupAutoRateEligibleSummary{}, nil
	}
	// 口径对齐列表 active+rate_limited：排除已过期账号，但保留临时限流/过载账号以免倍率在窗口期内抖动失真。
	rows, err := exec.QueryContext(ctx, `
		SELECT a.id, a.name, a.rate_multiplier, COUNT(*) OVER() AS eligible_count
		FROM account_groups ag
		JOIN accounts a ON a.id = ag.account_id
		WHERE ag.group_id = $1
		  AND a.deleted_at IS NULL
		  AND a.status = $2
		  AND a.schedulable = TRUE
		  AND (a.expires_at IS NULL OR a.expires_at > NOW() OR a.auto_pause_on_expired = FALSE)
		  AND a.rate_multiplier >= 0
		  AND (NOT $3 OR a.type <> $4)
		ORDER BY a.rate_multiplier DESC, a.id ASC
		LIMIT 1
	`, groupID, service.StatusActive, requireOAuthOnly, service.AccountTypeAPIKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	summary := &service.GroupAutoRateEligibleSummary{}
	if rows.Next() {
		var (
			accountID int64
			name      string
			rate      float64
			count     int64
		)
		if err := rows.Scan(&accountID, &name, &rate, &count); err != nil {
			return nil, err
		}
		summary.EligibleAccountCount = count
		summary.MaxAccountID = &accountID
		summary.MaxAccountName = name
		summary.MaxAccountRate = &rate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summary, nil
}

func (r *groupAutoRateRepository) SyncAutoRateForAccounts(ctx context.Context, accountIDs []int64) ([]int64, error) {
	ids := dedupePositiveInt64s(accountIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	exec := r.queryExecutor(ctx)
	if exec == nil {
		return nil, nil
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT DISTINCT g.id
		FROM account_groups ag
		JOIN groups g ON g.id = ag.group_id
		WHERE ag.account_id = ANY($1)
		  AND g.deleted_at IS NULL
		  AND g.platform = $2
		  AND g.subscription_type = $3
		  AND g.auto_rate_enabled = TRUE
		ORDER BY g.id
	`, pq.Array(ids), service.PlatformOpenAI, service.SubscriptionTypeStandard)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var groupIDs []int64
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		groupIDs = append(groupIDs, gid)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(groupIDs) == 0 {
		return nil, nil
	}
	return r.SyncAutoRateForGroups(ctx, groupIDs)
}

func (r *groupAutoRateRepository) SyncAutoRateForGroups(ctx context.Context, groupIDs []int64) ([]int64, error) {
	ids := dedupePositiveInt64s(groupIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	exec := r.queryExecutor(ctx)
	if exec == nil {
		return nil, nil
	}

	// Fast read-only precheck avoids opening a write transaction when none of the
	// target groups are OpenAI standard groups with auto_rate_enabled = TRUE.
	candidateIDs, err := r.findAutoRateCandidateGroupIDs(ctx, exec, ids)
	if err != nil {
		return nil, err
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	var (
		tx       *dbent.Tx
		txClient *dbent.Client
		txExec   sqlExecutor
	)
	if existingTx := dbent.TxFromContext(ctx); existingTx != nil {
		txClient = existingTx.Client()
		txExec = txClient
	} else if r.client != nil {
		openedTx, txErr := r.client.Tx(ctx)
		if errors.Is(txErr, dbent.ErrTxStarted) {
			txClient = r.client
			txExec = txClient
		} else if txErr != nil {
			return nil, txErr
		} else {
			tx = openedTx
			defer func() { _ = tx.Rollback() }()
			txClient = tx.Client()
			txExec = txClient
		}
	} else {
		txExec = exec
	}

	txCtx := ctx
	if tx != nil {
		txCtx = dbent.NewTxContext(ctx, tx)
	}

	lockedGroups, err := r.lockAutoRateGroups(txCtx, txExec, candidateIDs)
	if err != nil {
		return nil, err
	}
	if len(lockedGroups) == 0 {
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	lockedIDs := make([]int64, 0, len(lockedGroups))
	for _, g := range lockedGroups {
		lockedIDs = append(lockedIDs, g.id)
	}
	maxRates, err := r.queryMaxEligibleAccountRates(txCtx, txExec, lockedIDs)
	if err != nil {
		return nil, err
	}

	var changed []int64
	for _, g := range lockedGroups {
		maxRate, hasEligible := maxRates[g.id]
		if !hasEligible {
			continue
		}
		computed, ok := service.ComputeGroupAutoRate(maxRate, g.mode, g.value)
		if !ok {
			slog.Warn("group_auto_rate_sync_skipped",
				"group_id", g.id,
				"max_account_rate", maxRate,
				"auto_rate_mode", g.mode,
				"auto_rate_value", g.value,
				"current_rate_multiplier", g.rateMultiplier,
			)
			continue
		}
		if math.Abs(computed-g.rateMultiplier) <= groupAutoRateEqualityEpsilon {
			continue
		}
		if _, err := txExec.ExecContext(txCtx, `
			UPDATE groups
			SET rate_multiplier = $1,
				updated_at = NOW()
			WHERE id = $2
			  AND deleted_at IS NULL
		`, computed, g.id); err != nil {
			return nil, err
		}
		gid := g.id
		if err := enqueueSchedulerOutbox(txCtx, txExec, service.SchedulerOutboxEventGroupChanged, nil, &gid, nil); err != nil {
			return nil, err
		}
		changed = append(changed, g.id)
		slog.Info("group_auto_rate_sync_applied",
			"group_id", g.id,
			"old_rate_multiplier", g.rateMultiplier,
			"new_rate_multiplier", computed,
			"max_account_rate", maxRate,
			"auto_rate_mode", g.mode,
			"auto_rate_value", g.value,
		)
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return changed, nil
}

type lockedAutoRateGroup struct {
	id             int64
	rateMultiplier float64
	mode           string
	value          float64
}

func (r *groupAutoRateRepository) findAutoRateCandidateGroupIDs(ctx context.Context, exec sqlExecutor, groupIDs []int64) ([]int64, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT id
		FROM groups
		WHERE id = ANY($1)
		  AND deleted_at IS NULL
		  AND platform = $2
		  AND subscription_type = $3
		  AND auto_rate_enabled = TRUE
		ORDER BY id
	`, pq.Array(groupIDs), service.PlatformOpenAI, service.SubscriptionTypeStandard)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *groupAutoRateRepository) lockAutoRateGroups(ctx context.Context, exec sqlExecutor, groupIDs []int64) ([]lockedAutoRateGroup, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT id, rate_multiplier, auto_rate_mode, auto_rate_value
		FROM groups
		WHERE id = ANY($1)
		  AND deleted_at IS NULL
		  AND platform = $2
		  AND subscription_type = $3
		  AND auto_rate_enabled = TRUE
		ORDER BY id
		FOR UPDATE
	`, pq.Array(groupIDs), service.PlatformOpenAI, service.SubscriptionTypeStandard)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var groups []lockedAutoRateGroup
	for rows.Next() {
		var item lockedAutoRateGroup
		if err := rows.Scan(&item.id, &item.rateMultiplier, &item.mode, &item.value); err != nil {
			return nil, err
		}
		groups = append(groups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func (r *groupAutoRateRepository) queryMaxEligibleAccountRates(ctx context.Context, exec sqlExecutor, groupIDs []int64) (map[int64]float64, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT ag.group_id, MAX(a.rate_multiplier) AS max_rate
		FROM account_groups ag
		JOIN accounts a ON a.id = ag.account_id
		WHERE ag.group_id = ANY($1)
		  AND a.deleted_at IS NULL
		  AND a.status = $2
		  AND a.schedulable = TRUE
		  AND (a.expires_at IS NULL OR a.expires_at > NOW() OR a.auto_pause_on_expired = FALSE)
		  AND a.rate_multiplier >= 0
		GROUP BY ag.group_id
	`, pq.Array(groupIDs), service.StatusActive)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make(map[int64]float64, len(groupIDs))
	for rows.Next() {
		var (
			gid     int64
			maxRate float64
		)
		if err := rows.Scan(&gid, &maxRate); err != nil {
			return nil, err
		}
		result[gid] = maxRate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func dedupePositiveInt64s(input []int64) []int64 {
	if len(input) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(input))
	out := make([]int64, 0, len(input))
	for _, id := range input {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
