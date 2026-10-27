import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import { isApiError, setUnauthorizedHandler } from "./api/client";
import { qk } from "./api/queries";
import { ToastProvider } from "./components/toast";
import { Spinner } from "./components/ui";
import { RequireAuth } from "./layout/require-auth";
import { RouteError } from "./layout/route-error";
import { LoginPage } from "./pages/login";
import { NotFoundPage } from "./pages/not-found";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The event stream keeps caches current; staleTime only bounds how long
      // a page may show data from before a missed event.
      staleTime: 30_000,
      refetchOnWindowFocus: true,
      retry: (count, error) => !(isApiError(error) && error.status >= 400 && error.status < 500) && count < 2,
    },
  },
});

setUnauthorizedHandler(() => {
  void queryClient.resetQueries({ queryKey: qk.me() });
});

const lazyPage = <K extends string>(load: () => Promise<Record<K, React.ComponentType>>, name: K) => ({
  lazy: async () => ({ Component: (await load())[name] }),
});

const router = createBrowserRouter([
  { path: "/login", Component: LoginPage, ErrorBoundary: RouteError, HydrateFallback: Spinner },
  {
    path: "/",
    Component: RequireAuth,
    ErrorBoundary: RouteError,
    HydrateFallback: Spinner,
    children: [
      { index: true, ...lazyPage(() => import("./pages/overview"), "OverviewPage") },
      { path: "services", ...lazyPage(() => import("./pages/services"), "ServicesPage") },
      { path: "services/:name/:tab?", ...lazyPage(() => import("./pages/service-detail"), "ServiceDetailPage") },
      { path: "timeline", ...lazyPage(() => import("./pages/timeline"), "TimelinePage") },
      { path: "policies", ...lazyPage(() => import("./pages/policies"), "PoliciesPage") },
      { path: "policies/:name", ...lazyPage(() => import("./pages/policy-editor"), "PolicyEditorPage") },
      { path: "runs", ...lazyPage(() => import("./pages/runs"), "RunsPage") },
      { path: "runs/:id", ...lazyPage(() => import("./pages/run-detail"), "RunDetailPage") },
      { path: "hosts", ...lazyPage(() => import("./pages/hosts"), "HostsPage") },
      { path: "settings/:tab?", ...lazyPage(() => import("./pages/settings"), "SettingsPage") },
      { path: "*", Component: NotFoundPage },
    ],
  },
]);

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>
  );
}
