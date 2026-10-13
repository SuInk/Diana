export function allowNodeExecution(draft: string): string {
  const entries = draft.split(",").map((entry) => entry.trim()).filter(Boolean);
  let hasNode = false;
  const merged = entries.filter((entry) => {
    if (entry !== "node") return true;
    if (hasNode) return false;
    hasNode = true;
    return true;
  });
  if (!hasNode) merged.push("node");
  return merged.join(",");
}
