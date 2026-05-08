import { createFileRoute } from '@tanstack/react-router'
import { AccountPage } from '@/components/account/AccountPage'

export const Route = createFileRoute('/_app/account')({
  component: AccountPage,
})
