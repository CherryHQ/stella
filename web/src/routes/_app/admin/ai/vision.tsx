import { createFileRoute, redirect } from "@tanstack/react-router";

// The vision model is now one role on the deployment default-models page; keep
// the old admin link working.
export const Route = createFileRoute("/_app/admin/ai/vision")({
  beforeLoad: ({ location }) => {
    throw redirect({ href: `/admin/ai/models${location.searchStr}`, replace: true });
  },
});
