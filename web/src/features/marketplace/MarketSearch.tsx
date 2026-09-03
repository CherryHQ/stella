import { useEffect } from "react";
import { Search } from "lucide-react";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { targetValue } from "@/lib/utils";

/**
 * The marketplace search input every install sheet shares: it owns the
 * debounce, so callers keep their query state and receive the settled value
 * through `onDebounce` without wiring a timer themselves.
 */
export function MarketSearch({
  value,
  onValueChange,
  onDebounce,
  placeholder,
  delay = 250,
}: {
  value: string;
  onValueChange: (value: string) => void;
  onDebounce: (value: string) => void;
  placeholder?: string;
  delay?: number;
}) {
  useEffect(() => {
    const id = setTimeout(() => onDebounce(value), delay);
    return () => clearTimeout(id);
  }, [value, delay, onDebounce]);
  return (
    <InputGroup>
      <InputGroupAddon>
        <Search />
      </InputGroupAddon>
      <InputGroupInput
        nativeInput
        type="search"
        value={value}
        onChange={(e) => onValueChange(targetValue(e))}
        placeholder={placeholder}
      />
    </InputGroup>
  );
}
