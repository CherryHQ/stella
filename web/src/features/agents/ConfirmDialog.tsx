import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogPopup,
  DialogTitle,
  DialogFooter,
  DialogHeader,
  DialogDescription,
} from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";

interface Props {
  message: string;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({ message, onConfirm, onCancel }: Props) {
  const { t } = useI18n();
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
    >
      <DialogPopup showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t("common.confirm")}</DialogTitle>
          <DialogDescription>{message}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="ghost" size="sm" onClick={onCancel} className="cursor-pointer">
            {t("common.cancel")}
          </Button>
          <Button variant="destructive" size="sm" onClick={onConfirm} className="cursor-pointer">
            {t("common.delete")}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  );
}
