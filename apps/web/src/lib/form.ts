export function formValues(form: HTMLFormElement): Record<string, string | number> {
  const values: Record<string, string | number> = {};
  for (const field of form.elements) {
    if (!(field instanceof HTMLInputElement) || !field.name || field.value.trim() === "") continue;
    values[field.name] = field.type === "number" ? field.valueAsNumber : field.value.trim();
  }
  return values;
}
