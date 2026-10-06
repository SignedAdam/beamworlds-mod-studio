import { QueryCache, QueryClient, queryOptions } from "@tanstack/react-query";
import { CancelError, type CancellablePromise } from "@wailsio/runtime";
import { AppService as API } from "../bindings/github.com/SignedAdam/beamng-mod-studio/index.js";
import type {
  LibraryItem,
  OrganizationState,
} from "../bindings/github.com/SignedAdam/beamng-mod-studio/models.js";

// Server state lives here. Data is fetched once and stays fresh until a
// mutation or backend event invalidates its key; nothing refetches on focus
// or on a timer. Keys are hierarchical, so invalidating ["library"] refreshes
// every filtered list.

let reportError: (error: unknown) => void = () => {};

/** Routes failed queries to the app's error toast. */
export function setQueryErrorReporter(report: (error: unknown) => void) {
  reportError = report;
}

export const queryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (error) => {
      if (!(error instanceof CancelError)) reportError(error);
    },
  }),
  defaultOptions: {
    queries: {
      staleTime: Infinity,
      retry: false,
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
    },
  },
});

export type LibraryScope = "active" | "archived";

export interface LibraryFilter {
  query: string;
  collectionID: string;
  scope: LibraryScope;
}

/** The unfiltered active library: the catalog other pages resolve mods against. */
export const catalogFilter: LibraryFilter = { query: "", collectionID: "all", scope: "active" };


export const queryKeys = {
  library: ["library"] as const,
  libraryLists: ["library", "list"] as const,
  organization: ["organization"] as const,
  families: ["families"] as const,
  dashboard: ["dashboard"] as const,
  config: ["config"] as const,
  workspaces: ["workspaces"] as const,
};

/** Cancels the Wails call when the query is superseded or unmounted. */
function cancellable<T>(request: CancellablePromise<T>, signal: AbortSignal): Promise<T> {
  signal.addEventListener("abort", () => void request.cancel(), { once: true });
  return request;
}

export const libraryListQuery = (filter: LibraryFilter) =>
  queryOptions({
    queryKey: [...queryKeys.libraryLists, filter],
    queryFn: async ({ signal }) =>
      (await cancellable(API.ListLibrary("all", "all", filter.query, filter.collectionID, filter.scope), signal)) ?? [],
  });


export const organizationQuery = queryOptions({
  queryKey: queryKeys.organization,
  queryFn: () => API.Organization(),
});

export const familiesQuery = queryOptions({
  queryKey: queryKeys.families,
  queryFn: async () => (await API.ModFamilies()) ?? [],
});

export const dashboardQuery = queryOptions({
  queryKey: queryKeys.dashboard,
  queryFn: () => API.Dashboard(),
});

export const configQuery = queryOptions({
  queryKey: queryKeys.config,
  queryFn: () => API.Config(),
});

export const workspacesQuery = queryOptions({
  queryKey: queryKeys.workspaces,
  queryFn: async () => (await API.ListWorkspaces()) ?? [],
});

/** Applies an edit to every cached library list. */
export function updateCachedLibraryItems(update: (item: LibraryItem) => LibraryItem) {
  queryClient.setQueriesData<LibraryItem[]>({ queryKey: queryKeys.libraryLists }, (items) => items?.map(update));
}

/** Replaces one mod in every cached library list. */
export function replaceCachedLibraryItem(next: LibraryItem) {
  updateCachedLibraryItems((item) => (item.entityId === next.entityId ? next : item));
}

export function setCachedOrganization(
  update: OrganizationState | ((current: OrganizationState) => OrganizationState),
) {
  queryClient.setQueryData<OrganizationState>(queryKeys.organization, (current) =>
    typeof update === "function" ? (current ? update(current) : current) : update,
  );
}
