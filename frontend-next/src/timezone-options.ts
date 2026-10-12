// 固定 UTC 偏移不采用夏令时；已有地区时区保留原值与地区规则。
export function timezoneOptions(current: string | undefined, defaultLabel: string) {
  const options = [{ value: "", label: defaultLabel }];
  for (let minutes = -12 * 60; minutes <= 14 * 60; minutes += 15) {
    const absolute = Math.abs(minutes);
    const suffix = absolute % 60 ? `:${String(absolute % 60).padStart(2, "0")}` : "";
    const value = minutes === 0 ? "UTC" : `UTC${minutes < 0 ? "-" : "+"}${Math.floor(absolute / 60)}${suffix}`;
    options.push({ value, label: minutes === 0 ? "UTC+0" : value });
  }
  if (current && !options.some((option) => option.value === current)) {
    options.push({ value: current, label: `${current}（现有地区时区）` });
  }
  return options;
}
