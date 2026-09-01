import {
  Combobox,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxPopup,
} from "@/components/ui/combobox";

export interface ProviderSearchOption {
  value: string;
  label: string;
  description?: string;
  disabled?: boolean;
}

export function ProviderSearchCombobox({
  value,
  options,
  placeholder,
  emptyText,
  disabled = false,
  ariaLabel,
  onChange,
}: {
  value: string;
  options: ProviderSearchOption[];
  placeholder: string;
  emptyText: string;
  disabled?: boolean;
  ariaLabel: string;
  onChange: (value: string) => void;
}) {
  const selected = options.find((option) => option.value === value) ?? null;

  return (
    <Combobox
      items={options}
      value={selected}
      disabled={disabled}
      onValueChange={(option) => option && onChange(option.value)}
    >
      <ComboboxInput placeholder={placeholder} aria-label={ariaLabel} showClear={false} />
      <ComboboxPopup>
        <ComboboxEmpty>{emptyText}</ComboboxEmpty>
        <ComboboxList>
          {(option: ProviderSearchOption) => (
            <ComboboxItem key={option.value} value={option} disabled={option.disabled}>
              <div className="min-w-0">
                <div className="truncate">{option.label}</div>
                {option.description && (
                  <div className="truncate font-mono text-xs text-muted-foreground">
                    {option.description}
                  </div>
                )}
              </div>
            </ComboboxItem>
          )}
        </ComboboxList>
      </ComboboxPopup>
    </Combobox>
  );
}
