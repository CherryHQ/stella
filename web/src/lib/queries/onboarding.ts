import { queryOptions } from "@tanstack/react-query";
import { getOnboardingStatus } from "@/lib/api-client/sdk.gen";

export const onboardingQueryOptions = queryOptions({
  queryKey: ["onboarding"],
  queryFn: async () => {
    const { data } = await getOnboardingStatus({ throwOnError: true });
    return data;
  },
  retry: false,
});
