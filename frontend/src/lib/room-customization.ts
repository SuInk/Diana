// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

export const ROOM_IMAGE_LIMIT = 700 * 1024;
export const roomImageSlots = ["background", "rest", "phone", "desk", "walk"] as const;
export type RoomImageSlot = typeof roomImageSlots[number];
export interface RoomAppearance {
  version: 1;
  images: Partial<Record<RoomImageSlot, string>>;
  size: number;
  bedX: number;
  bedY: number;
  deskX: number;
  deskY: number;
}
export function defaultRoomAppearance(): RoomAppearance {
  return { version: 1, images: {}, size: 27, bedX: 73, bedY: 78, deskX: 29, deskY: 76 };
}
export function roomAppearanceKey(profile: string): string {
  if (!profile.trim()) throw new Error("请先选择机器人");
  return `diana:room-appearance:v1:${encodeURIComponent(profile)}`;
}
export function normalizeRoomAppearance(value: unknown): RoomAppearance {
  const result = defaultRoomAppearance();
  if (!value || typeof value !== "object") return result;
  const data = value as Record<string, unknown>;
  if (data.version !== 1) return result;
  for (const key of ["size", "bedX", "bedY", "deskX", "deskY"] as const) {
    const n = data[key];
    if (typeof n === "number" && Number.isFinite(n)) result[key] = Math.min(key === "size" ? 45 : 90, Math.max(key === "size" ? 15 : 10, n));
  }
  if (data.images && typeof data.images === "object") {
    for (const slot of roomImageSlots) {
      const image = (data.images as Record<string, unknown>)[slot];
      if (typeof image === "string" && image.length <= ROOM_IMAGE_LIMIT * 1.34 + 50 && /^data:image\/(png|jpeg|webp);base64,[A-Za-z0-9+/]+=*$/.test(image)) result.images[slot] = image;
    }
  }
  return result;
}

/** Decode locally: never accept SVG/HTML, remote URLs or unbounded image files. */
export async function readRoomImage(file: File): Promise<string> {
  if (!["image/png", "image/jpeg", "image/webp"].includes(file.type)) throw new Error("只支持 PNG、JPEG、WebP 图片");
  if (file.size > ROOM_IMAGE_LIMIT) throw new Error("每张图片不能超过 700 KB");
  const header = new Uint8Array(await file.slice(0, 12).arrayBuffer());
  const png = [137, 80, 78, 71, 13, 10, 26, 10].every((byte, i) => header[i] === byte);
  const jpeg = header[0] === 255 && header[1] === 216 && header[2] === 255;
  const webp = String.fromCharCode(...header.slice(0, 4)) === "RIFF" && String.fromCharCode(...header.slice(8, 12)) === "WEBP";
  if (!(file.type === "image/png" ? png : file.type === "image/jpeg" ? jpeg : webp)) throw new Error("图片内容与文件类型不匹配");
  const data = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new Error("无法读取图片"));
    reader.readAsDataURL(file);
  });
  await new Promise<void>((resolve, reject) => {
    const image = new Image();
    image.onload = () => image.width > 4096 || image.height > 4096 || image.width < 1 || image.height < 1
      ? reject(new Error("图片尺寸需在 1 至 4096 像素以内")) : resolve();
    image.onerror = () => reject(new Error("图片文件损坏或无法解码"));
    image.src = data;
  });
  return data;
}
