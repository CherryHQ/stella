import { getMeOptions } from "@/lib/api-client/@tanstack/react-query.gen";

export const meQueryOptions = {
  ...getMeOptions({ baseUrl: "" }),
  retry: false,
};
