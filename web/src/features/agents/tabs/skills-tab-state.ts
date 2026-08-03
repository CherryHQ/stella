export function activationControlState(
  logicalRef: string | undefined,
  canManage: boolean,
  pending: boolean,
) {
  return { visible: Boolean(logicalRef) && canManage, disabled: pending };
}

export function danglingClearControlState(canManage: boolean, pending: boolean) {
  return { visible: canManage, disabled: pending };
}
