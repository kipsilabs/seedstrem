import { ReactNode } from "react";

// Small presentational form helpers to keep the section components declarative.

export function SectionCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="surface p-6">
      <h2 className="text-lg font-bold tracking-brand">{title}</h2>
      {description && <p className="mt-1 text-sm opacity-70">{description}</p>}
      <div className="mt-4 flex flex-col gap-4">{children}</div>
    </div>
  );
}

interface TextFieldProps {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  type?: "text" | "password";
  hint?: ReactNode;
}

export function TextField({
  label,
  value,
  onChange,
  placeholder,
  type = "text",
  hint,
}: TextFieldProps) {
  return (
    <label className="flex flex-col gap-1">
      <span className="label-text mb-1">{label}</span>
      <input
        type={type}
        className="input input-bordered w-full"
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      {hint && <span className="label-text-alt mt-1 text-base-content/60">{hint}</span>}
    </label>
  );
}

interface NumberFieldProps {
  label: string;
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
  step?: number;
  placeholder?: string;
  hint?: ReactNode;
}

export function NumberField({
  label,
  value,
  onChange,
  min,
  max,
  step,
  placeholder,
  hint,
}: NumberFieldProps) {
  return (
    <label className="flex flex-col gap-1">
      <span className="label-text mb-1">{label}</span>
      <input
        type="number"
        className="input input-bordered w-full"
        min={min}
        max={max}
        step={step}
        placeholder={placeholder}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
      />
      {hint && <span className="label-text-alt mt-1 text-base-content/60">{hint}</span>}
    </label>
  );
}

interface SelectFieldOption {
  value: string;
  label: string;
}

interface SelectFieldProps {
  label: string;
  value: string;
  options: SelectFieldOption[];
  onChange: (v: string) => void;
  hint?: ReactNode;
}

export function SelectField({ label, value, options, onChange, hint }: SelectFieldProps) {
  return (
    <label className="flex flex-col gap-1">
      <span className="label-text mb-1">{label}</span>
      <select
        className="select select-bordered w-full"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      {hint && <span className="label-text-alt mt-1 text-base-content/60">{hint}</span>}
    </label>
  );
}

export function ToggleField({
  label,
  checked,
  onChange,
}: {
  label: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex cursor-pointer items-start gap-3">
      <input
        type="checkbox"
        className="toggle toggle-primary mt-0.5 shrink-0"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="text-sm leading-snug">{label}</span>
    </label>
  );
}
