// 原始字节展示工具：base64（后端 wire 格式）→ hex dump。
//
// 供「原始包」表与连接详情的「帧 / Raw」子页共用。此前两处各有一份拷贝，
// 其中一份在 hexDump 里用 chunk[offset + i] 索引（chunk 已是 slice，长度 ≤16），
// 第二行开始必然越界抛错 → 整个 dump 退化成 "(decode error)"。统一到这一份。

/** base64 → 字节数组。 */
export function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** base64 → 字节数（非法输入返回 0）。 */
export function base64ByteLen(b64: string): number {
  try {
    return base64ToBytes(b64).length;
  } catch {
    return 0;
  }
}

/** 字节 → hex dump（偏移 | hex | ascii）。 */
export function hexDump(bytes: Uint8Array, maxBytes = 4096): string {
  const truncated = bytes.length > maxBytes;
  const slice = truncated ? bytes.slice(0, maxBytes) : bytes;
  const lines: string[] = [];
  for (let offset = 0; offset < slice.length; offset += 16) {
    const chunk = slice.slice(offset, offset + 16);
    const hexParts: string[] = [];
    const asciiParts: string[] = [];
    for (let i = 0; i < 16; i++) {
      if (i < chunk.length) {
        const byte = chunk[i]!;
        hexParts.push(byte.toString(16).padStart(2, "0"));
        asciiParts.push(byte >= 0x20 && byte < 0x7f ? String.fromCharCode(byte) : ".");
      } else {
        hexParts.push("  ");
        asciiParts.push(" ");
      }
    }
    const offsetStr = offset.toString(16).padStart(8, "0");
    const hexStr = hexParts.slice(0, 8).join(" ") + "  " + hexParts.slice(8).join(" ");
    lines.push(`${offsetStr}  ${hexStr}  |${asciiParts.join("")}|`);
  }
  if (truncated) {
    lines.push(`... (${bytes.length} bytes total, showing first ${maxBytes})`);
  }
  return lines.join("\n");
}

/** base64 → 单行 hex 预览（表格行内用，超长截断）。 */
export function hexPreview(b64: string, maxLen = 32): string {
  try {
    const bytes = base64ToBytes(b64);
    const hex: string[] = [];
    for (let i = 0; i < Math.min(bytes.length, maxLen); i++) {
      hex.push(bytes[i]!.toString(16).padStart(2, "0"));
    }
    return hex.join(" ") + (bytes.length > maxLen ? " …" : "");
  } catch {
    return "(decode error)";
  }
}
