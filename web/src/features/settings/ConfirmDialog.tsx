import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogPopup,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  message,
  onConfirm,
  variant = "destructive",
  confirmLabel,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  message: string;
  onConfirm: () => void;
  variant?: "destructive" | "default";
  confirmLabel?: string;
}) {
  const { t } = useI18n();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <div className="px-6 pb-2">
          <p className="text-sm">{message}</p>
        </div>
        <DialogFooter>
          <Button onClick={() => onOpenChange(false)} variant="ghost" size="sm">
            {t("common.cancel")}
          </Button>
          <Button
            onClick={() => {
              onOpenChange(false);
              onConfirm();
            }}
            variant={variant}
            size="sm"
          >
            {confirmLabel || (variant === "destructive" ? t("common.delete") : t("common.confirm"))}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}
