import { defineComponent } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGroup } from "@/types";
import GroupsView from "@/views/admin/GroupsView.vue";
import { computeGroupAutoRate } from "@/views/admin/groupsAutoRate";

const {
  listGroups,
  createGroup,
  updateGroup,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  getAutoRatePreview,
  recalculateAutoRate,
  showSuccess,
  showError,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  getAutoRatePreview: vi.fn(),
  recalculateAutoRate: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}));

const authState = vi.hoisted(() => ({ isSimpleMode: false }));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAutoRatePreview,
      recalculateAutoRate,
      create: createGroup,
      update: updateGroup,
      delete: vi.fn(),
      duplicate: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}));

vi.mock("@/stores/auth", () => ({
  useAuthStore: () => authState,
}));

vi.mock("@/stores/onboarding", () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
  };
});

const baseOpenAIGroup = {
  id: 42,
  name: "OpenAI-Auto",
  description: null,
  platform: "openai",
  rate_multiplier: 0.18,
  rpm_limit: 0,
  is_exclusive: false,
  status: "active",
  subscription_type: "standard",
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  long_context_pricing_enabled: true,
  force_openai_fast: false,
  force_openai_ultrafast: false,
  free_openai_fast: false,
  model_pricing: [],
  profit_control_enabled: false,
  profit_min_margin: 0,
  profit_safety_buffer: 0,
  cache_hit_rate_adjustment: 0,
  auto_rate_enabled: true,
  auto_rate_mode: "percentage",
  auto_rate_value: 0.2,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_count_billing: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  search_price_per_1k: null,
  audio_realtime_price_per_min: null,
  audio_tts_price_per_million_chars: null,
  audio_stt_price_per_hour: null,
  peak_rate_enabled: false,
  peak_start: "",
  peak_end: "",
  peak_rate_multiplier: 1,
  off_peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: "2026-07-16T00:00:00Z",
  updated_at: "2026-07-16T00:00:00Z",
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 2,
  active_account_count: 2,
  rate_limited_account_count: 0,
  sort_order: 10,
} satisfies AdminGroup;

const AppLayoutStub = defineComponent({
  template: "<main><slot /></main>",
});

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
});

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
  },
  template:
    '<div><div v-for="row in data" :key="row.id" :data-row-id="row.id"><slot name="cell-rate_multiplier" :row="row" :value="row.rate_multiplier" /><slot name="cell-actions" :row="row" /></div></div>',
});

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
});

const SelectStub = defineComponent({
  props: {
    modelValue: { type: [String, Number, Boolean], default: "" },
    options: { type: Array, default: () => [] },
  },
  emits: ["update:modelValue"],
  template:
    '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="opt in options" :key="opt.value" :value="opt.value">{{ opt.label }}</option></select>',
});

const mountView = () =>
  mount(GroupsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true,
      },
    },
  });

describe("GroupsView auto group rate", () => {
  beforeEach(() => {
    authState.isSimpleMode = false;
    localStorage.clear();
    for (const fn of [
      listGroups,
      createGroup,
      updateGroup,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAutoRatePreview,
      recalculateAutoRate,
      showSuccess,
      showError,
    ]) {
      fn.mockReset();
    }

    listGroups.mockResolvedValue({
      items: [baseOpenAIGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    createGroup.mockResolvedValue({ ...baseOpenAIGroup, id: 43 });
    updateGroup.mockResolvedValue(baseOpenAIGroup);
    getModelAllowlistCandidates.mockResolvedValue([]);
    getUsageSummary.mockResolvedValue([]);
    getCapacitySummary.mockResolvedValue([]);
    getLiveCapability.mockResolvedValue({ supported: false });
    getAutoRatePreview.mockResolvedValue({
      supported: true,
      eligible_account_count: 2,
      max_account_id: 9,
      max_account_name: "acc-top",
      max_account_rate: 0.15,
      mode: "percentage",
      value: 0.2,
      computed_rate: 0.18,
      status: "ready",
    });
    recalculateAutoRate.mockResolvedValue({
      group: { ...baseOpenAIGroup, rate_multiplier: 0.3 },
      preview: {
        supported: true,
        eligible_account_count: 2,
        max_account_id: 9,
        max_account_name: "acc-top",
        max_account_rate: 0.25,
        mode: "percentage",
        value: 0.2,
        computed_rate: 0.3,
        status: "ready",
      },
    });
  });

  it("computes fixed-point ceiling rates without floating-point drift", () => {
    expect(computeGroupAutoRate(0.15, "percentage", 0.2)).toBe(0.18);
    expect(computeGroupAutoRate(0.13, "percentage", 0.15)).toBe(0.15);
    expect(computeGroupAutoRate(0.3333, "percentage", 0.15)).toBe(0.39);
    expect(computeGroupAutoRate(0.0001, "percentage", 0)).toBe(0.01);
    expect(computeGroupAutoRate(0, "fixed", 0.2)).toBe(0.2);
    expect(computeGroupAutoRate(0, "percentage", 0.2)).toBeNull();
    expect(computeGroupAutoRate(90, "percentage", 0.2)).toBeNull();
  });

  it("renders auto rate badge in table and shows config only for openai standard", async () => {
    const wrapper = mountView();
    await flushPromises();

    const row = wrapper.get('[data-row-id="42"]');
    expect(row.text()).toContain("0.18x");
    expect(row.text()).toContain("admin.groups.autoRate.badge");
    expect(row.text()).toContain("+20%");

    // Open create modal (default platform is anthropic -> toggle absent)
    await wrapper.get('[data-tour="groups-create-btn"]').trigger("click");
    await flushPromises();
    expect(wrapper.find('[data-testid="create-auto-rate-toggle"]').exists()).toBe(false);

    // Switch platform to openai -> toggle appears
    await wrapper.get('#create-group-form select').setValue("openai");
    await flushPromises();
    expect(wrapper.find('[data-testid="create-auto-rate-toggle"]').exists()).toBe(true);

    // Enable auto rate with percentage 20 -> submits 0.2
    await wrapper.get('[data-tour="group-form-name"]').setValue("Auto-OpenAI");
    await wrapper.get('[data-testid="create-auto-rate-toggle"]').setValue(true);
    await wrapper.get('[data-testid="create-auto-rate-percent"]').setValue("20");
    await wrapper.get('button[type="submit"][form="create-group-form"]').trigger("click");
    await flushPromises();

    expect(createGroup).toHaveBeenCalledTimes(1);
    expect(createGroup.mock.calls[0][0]).toMatchObject({
      platform: "openai",
      auto_rate_enabled: true,
      auto_rate_mode: "percentage",
      auto_rate_value: 0.2,
    });
    wrapper.unmount();
  });

  it("locks rate_multiplier in edit modal, computes live preview, and recalculates on click", async () => {
    const wrapper = mountView();
    await flushPromises();

    const editButton = wrapper
      .findAll("button")
      .find((button) => button.text() === "common.edit");
    expect(editButton).toBeTruthy();
    await editButton!.trigger("click");
    await flushPromises();

    const rateInput = wrapper.get<HTMLInputElement>('#edit-group-form [data-tour="group-form-multiplier"]');
    expect(rateInput.element.disabled).toBe(true);
    expect(getAutoRatePreview).toHaveBeenCalledWith(42);

    const previewBox = wrapper.get('[data-testid="edit-auto-rate-preview"]');
    expect(previewBox.text()).toContain("0.18");

    // Switch to fixed +0.10 -> preview updates immediately to ceil(0.15 + 0.10) = 0.25
    await wrapper.get('[data-testid="edit-auto-rate-mode"]').setValue("fixed");
    await wrapper.get('[data-testid="edit-auto-rate-fixed"]').setValue("0.1");
    await flushPromises();
    expect(previewBox.text()).toContain("0.25");

    // Click recalculate button
    await wrapper.get('[data-testid="edit-auto-rate-recalculate"]').trigger("click");
    await flushPromises();
    expect(recalculateAutoRate).toHaveBeenCalledWith(42);
    expect(rateInput.element.value).toBe("0.3");

    // Save edit form -> rate_multiplier omitted while locked
    await wrapper.get('button[type="submit"][form="edit-group-form"]').trigger("click");
    await flushPromises();
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup.mock.calls[0][1]).not.toHaveProperty("rate_multiplier");
    expect(updateGroup.mock.calls[0][1]).toMatchObject({
      auto_rate_enabled: true,
      auto_rate_mode: "fixed",
      auto_rate_value: 0.1,
    });
    wrapper.unmount();
  });

  it("hides waiting-eligible badge when only rate-limited accounts exist and shows it when both counts are zero", async () => {
    listGroups.mockResolvedValue({
      items: [
        {
          ...baseOpenAIGroup,
          id: 43,
          active_account_count: 0,
          rate_limited_account_count: 2,
        },
        {
          ...baseOpenAIGroup,
          id: 44,
          active_account_count: 0,
          rate_limited_account_count: 0,
        },
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1,
    });

    const wrapper = mountView();
    await flushPromises();

    expect(wrapper.get('[data-row-id="43"]').text()).not.toContain("admin.groups.autoRate.waitingEligible");
    expect(wrapper.get('[data-row-id="44"]').text()).toContain("admin.groups.autoRate.waitingEligible");
    wrapper.unmount();
  });

  it("normalizes cleared rate_multiplier when locked in create form and passes require_oauth_only to preview", async () => {
    listGroups.mockResolvedValue({
      items: [baseOpenAIGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    const wrapper = mountView();
    await flushPromises();

    await wrapper.get('[data-tour="groups-create-btn"]').trigger("click");
    await flushPromises();

    await wrapper.get('#create-group-form select').setValue("openai");
    await flushPromises();

    // Clear rate_multiplier before locking it via auto-rate + copy source group
    const rateInput = wrapper.get<HTMLInputElement>('#create-group-form [data-tour="group-form-multiplier"]');
    await rateInput.setValue("");

    await wrapper.get('[data-tour="group-form-name"]').setValue("Auto-Copy-OpenAI");
    await wrapper.get('[data-testid="create-auto-rate-toggle"]').setValue(true);

    // Select source group to copy accounts from (locks rate_multiplier)
    const allSelects = wrapper.findAll('#create-group-form select');
    const sourceSelect = allSelects.find((s) =>
      s.findAll('option').some((opt) => opt.attributes('value') === '42'),
    );
    expect(sourceSelect).toBeTruthy();
    await sourceSelect!.setValue("42");
    await flushPromises();

    expect(rateInput.element.disabled).toBe(true);
    expect(getAutoRatePreview).toHaveBeenCalledWith(0, {
      source_group_id: 42,
      require_oauth_only: false,
    });

    await wrapper.get('button[type="submit"][form="create-group-form"]').trigger("click");
    await flushPromises();

    expect(createGroup).toHaveBeenCalledTimes(1);
    expect(createGroup.mock.calls[0][0].rate_multiplier).toBe(1);
    wrapper.unmount();
  });
});
