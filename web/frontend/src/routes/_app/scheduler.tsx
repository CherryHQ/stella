import { createFileRoute } from '@tanstack/react-router'
import { SchedulerPage } from '@/components/scheduler/SchedulerPage'

export const Route = createFileRoute('/_app/scheduler')({
  component: SchedulerPage,
})
