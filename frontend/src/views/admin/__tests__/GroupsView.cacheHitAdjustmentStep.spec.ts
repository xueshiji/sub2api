import { defineComponent } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGroup } from "@/types";
import GroupsView from "@/views/admin/GroupsView.vue";

const {
  listGroups,
  createGroup,
  updateGroup,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
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
    useI18n: () => ({ t: (key: string) => key }),
  };
});

const sourceGroup = {
  id: 42,
  name: "Primary",
  description: null,
  platform: "openai",
  rate_multiplier: 1,
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
  account_count: 1,
  active_account_count: 1,
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
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>',
});

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
});

const mountView = () =>
  mount(GroupsView, {
    // 提交按钮先过浏览器原生约束校验，表单必须连接到 document 才会走到 submit
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
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true,
      },
    },
  });

type GroupsViewWrapper = ReturnType<typeof mountView>;

const openCreateDialog = async (wrapper: GroupsViewWrapper) => {
  await wrapper.get('[data-tour="groups-create-btn"]').trigger("click");
  await flushPromises();
};

const openEditDialog = async (wrapper: GroupsViewWrapper) => {
  const editButton = wrapper
    .findAll("button")
    .find((button) => button.text() === "common.edit");
  expect(editButton).toBeTruthy();
  await editButton!.trigger("click");
  await flushPromises();
};

const createAdjustment = '#create-group-form input[type="number"][max="10"]';
const createSubmit = 'button[type="submit"][form="create-group-form"]';
const editAdjustment = '#edit-group-form input[type="number"][max="10"]';
const editSubmit = 'button[type="submit"][form="edit-group-form"]';

describe("GroupsView cache hit rate adjustment step", () => {
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
      showSuccess,
      showError,
    ]) {
      fn.mockReset();
    }

    listGroups.mockResolvedValue({
      items: [sourceGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    createGroup.mockResolvedValue({ ...sourceGroup, id: 43 });
    updateGroup.mockResolvedValue(sourceGroup);
    getModelAllowlistCandidates.mockResolvedValue([]);
    getUsageSummary.mockResolvedValue([]);
    getCapacitySummary.mockResolvedValue([]);
    getLiveCapability.mockResolvedValue({ supported: false });
  });

  it("submits the create form with a two-decimal cache hit rate adjustment", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openCreateDialog(wrapper);

    await wrapper.get('[data-tour="group-form-name"]').setValue("Adjusted");
    await wrapper.get(createAdjustment).setValue("2.55");
    await wrapper.get(createSubmit).trigger("click");
    await flushPromises();

    expect(createGroup).toHaveBeenCalledTimes(1);
    expect(createGroup.mock.calls[0][0]).toMatchObject({
      cache_hit_rate_adjustment: 0.0255,
    });
    wrapper.unmount();
  });

  it("keeps the create form from submitting a three-decimal cache hit rate adjustment", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openCreateDialog(wrapper);

    await wrapper.get('[data-tour="group-form-name"]').setValue("Adjusted");
    await wrapper.get(createAdjustment).setValue("2.555");
    await wrapper.get(createSubmit).trigger("click");
    await flushPromises();

    expect(createGroup).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("submits the edit form with a two-decimal cache hit rate adjustment", async () => {
    const wrapper = mountView();
    await flushPromises();
    await openEditDialog(wrapper);

    await wrapper.get(editAdjustment).setValue("2.55");
    await wrapper.get(editSubmit).trigger("click");
    await flushPromises();

    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup.mock.calls[0][0]).toBe(sourceGroup.id);
    expect(updateGroup.mock.calls[0][1]).toMatchObject({
      cache_hit_rate_adjustment: 0.0255,
    });
    wrapper.unmount();
  });

  it("saves a group stored with a four-decimal cache hit rate adjustment without touching the field", async () => {
    listGroups.mockResolvedValue({
      items: [{ ...sourceGroup, cache_hit_rate_adjustment: 0.0255 }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    const wrapper = mountView();
    await flushPromises();
    await openEditDialog(wrapper);

    expect(wrapper.get<HTMLInputElement>(editAdjustment).element.value).toBe("2.55");
    await wrapper.get('[data-tour="edit-group-form-name"]').setValue("Renamed");
    await wrapper.get(editSubmit).trigger("click");
    await flushPromises();

    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup.mock.calls[0][1]).toMatchObject({
      name: "Renamed",
      cache_hit_rate_adjustment: 0.0255,
    });
    wrapper.unmount();
  });
});
