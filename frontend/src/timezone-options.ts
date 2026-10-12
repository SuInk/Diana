// 地区时区保存 IANA 名，由运行时按日期应用夏令时；固定 UTC 偏移全年不变。
interface TimezoneOption {
  value: string;
  label: string;
  hint?: string;
  group?: string;
}

const commonRegions: Record<string, string> = {
  "Asia/Shanghai": "北京 / 上海",
  "Asia/Hong_Kong": "香港",
  "Asia/Taipei": "台北",
  "Asia/Tokyo": "东京",
  "Asia/Seoul": "首尔",
  "Asia/Singapore": "新加坡",
  "Asia/Kolkata": "新德里",
  "Asia/Kathmandu": "加德满都",
  "Asia/Dubai": "迪拜",
  "Europe/London": "伦敦",
  "Europe/Paris": "巴黎",
  "Europe/Berlin": "柏林",
  "Europe/Moscow": "莫斯科",
  "America/New_York": "纽约",
  "America/Chicago": "芝加哥",
  "America/Denver": "丹佛",
  "America/Los_Angeles": "洛杉矶",
  "America/Phoenix": "凤凰城",
  "America/Toronto": "多伦多",
  "America/Vancouver": "温哥华",
  "America/Sao_Paulo": "圣保罗",
  "Pacific/Honolulu": "檀香山",
  "Australia/Sydney": "悉尼",
  "Australia/Adelaide": "阿德莱德",
  "Australia/Perth": "珀斯",
  "Pacific/Auckland": "奥克兰"
};

// 较旧浏览器没有 supportedValuesOf，仍提供常用地区以及已保存的地区。
const supportedValuesOf = (Intl as typeof Intl & {
  supportedValuesOf?: (key: "timeZone") => string[];
}).supportedValuesOf;
const regionIDs = [...new Set([...Object.keys(commonRegions), ...(supportedValuesOf?.("timeZone") ?? [])])];
const formatters = new Map<string, Intl.DateTimeFormat>();

function regionOption(value: string, now: Date): TimezoneOption {
  let offset = "";
  try {
    let formatter = formatters.get(value);
    if (!formatter) {
      formatter = new Intl.DateTimeFormat("en-US", {
        timeZone: value,
        // shortOffset 由浏览器计算当日真实偏移，不能用固定的冬令时数值代替。
        timeZoneName: "shortOffset" as Intl.DateTimeFormatOptions["timeZoneName"]
      });
      formatters.set(value, formatter);
    }
    const zone = formatter.formatToParts(now).find((part) => part.type === "timeZoneName")?.value;
    if (zone?.startsWith("GMT")) offset = zone === "GMT" ? "UTC+0" : zone.replace("GMT", "UTC");
  } catch {
    // 展示能力不足或旧配置名称不被浏览器识别时，仍保留原值交给后端解析。
  }
  return {
    value,
    label: `${offset ? `当前 ${offset} · ` : ""}${commonRegions[value] ?? value}`,
    hint: `${value} · 自动遵循地区规则（含夏令时，如适用）`,
    group: "地区时区 · 随夏令时自动调整"
  };
}

let regionCache: { minute: number; options: TimezoneOption[] } | undefined;

export function timezoneOptions(current: string | undefined, defaultLabel: string, now = new Date()): TimezoneOption[] {
  const minute = Math.floor(now.getTime() / 60000);
  if (!regionCache || regionCache.minute !== minute) {
    regionCache = { minute, options: regionIDs.map((value) => regionOption(value, now)) };
  }
  const options: TimezoneOption[] = [{ value: "", label: defaultLabel }, ...regionCache.options];
  // 别把已保存的地区静默换成当前偏移，否则下次夏令时切换就错了。
  if (current && current !== "UTC" && !/^UTC[+-]/.test(current) && !regionIDs.includes(current)) {
    options.push(regionOption(current, now));
  }
  for (let minutes = -12 * 60; minutes <= 14 * 60; minutes += 15) {
    const absolute = Math.abs(minutes);
    const suffix = absolute % 60 ? `:${String(absolute % 60).padStart(2, "0")}` : "";
    const value = minutes === 0 ? "UTC" : `UTC${minutes < 0 ? "-" : "+"}${Math.floor(absolute / 60)}${suffix}`;
    options.push({
      value,
      label: minutes === 0 ? "UTC+0" : value,
      hint: "固定偏移，全年不随夏令时变化",
      group: "固定 UTC 偏移 · 不使用夏令时"
    });
  }
  if (current && !options.some((option) => option.value === current)) {
    options.push({ value: current, label: current, hint: "现有配置，保持原值" });
  }
  return options;
}
