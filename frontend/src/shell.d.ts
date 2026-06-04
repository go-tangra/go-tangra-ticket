declare module 'shell/vben/stores' {
  import type { StoreDefinition } from 'pinia';
  export const useAccessStore: StoreDefinition;
  export const useUserStore: StoreDefinition;
}

declare module 'shell/vben/common-ui' {
  import type { Component } from 'vue';
  export const Page: Component;
  export function useVbenDrawer(options: any): [Component, any];
  export function useVbenModal(options: any): [Component, any];
  export function useVbenForm(options: any): [Component, any];
  export type VbenFormProps = any;
}

declare module 'shell/vben/icons' {
  import type { Component } from 'vue';
  export const LucideEye: Component;
  export const LucideTrash: Component;
  export const LucideTrash2: Component;
  export const LucidePencil: Component;
  export const LucideFilePenLine: Component;
  export const LucidePlus: Component;
  export const LucideRefreshCw: Component;
  export const LucideXCircle: Component;
  export const LucideCheckCircle: Component;
  export const LucideShield: Component;
  export const LucideShare2: Component;
  export const LucideUser: Component;
  export const LucideUsers: Component;
  export const LucideBell: Component;
  export const LucideRadio: Component;
  export const LucideFileText: Component;
  export const LucideScrollText: Component;
}

declare module 'shell/vben/layouts' {
  import type { Component } from 'vue';
  export const BasicLayout: Component;
}

declare module 'shell/app-layout' {
  import type { Component } from 'vue';
  const component: Component;
  export default component;
}

declare module 'shell/adapter/vxe-table' {
  export function useVbenVxeGrid(options: any): any;
  export type VxeGridProps = any;
}

declare module 'shell/adapter/form' {
  export function useVbenForm(options: any): [any, any];
}

declare module 'shell/locales' {
  export function $t(key: string, ...args: any[]): string;
}
