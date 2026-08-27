import { createFileRoute, redirect } from "@tanstack/react-router";

// The embedding model and its lane knobs are now one section of the deployment
// models page; keep the old admin link working.
export const Route = createFileRoute("/_app/admin/ai/embedding")({
  beforeLoad: ({ location }) => {
    throw redirect({ href: `/admin/ai/models${location.searchStr}`, replace: true });
  },
});
