/** Uncompressed ZIP32 archive with UTF-8 names; no extra runtime dependency. */
export interface ZipFile { name: string; bytes: Uint8Array }
const table = Uint32Array.from({ length: 256 }, (_, n) => {
  for (let bit = 0; bit < 8; bit++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1
  return n >>> 0
})
function crc32(bytes: Uint8Array) {
  let crc = 0xffffffff
  for (const byte of bytes) crc = table[(crc ^ byte) & 255] ^ (crc >>> 8)
  return (crc ^ 0xffffffff) >>> 0
}
export function createStoredZip(files: ZipFile[]): Uint8Array<ArrayBuffer> {
  if (files.length > 65535) throw new RangeError('Too many ZIP entries')
  const entries = files.map(file => ({ ...file, nameBytes: new TextEncoder().encode(file.name), crc: crc32(file.bytes) }))
  let localSize = 0, centralSize = 0
  for (const file of entries) {
    if (file.nameBytes.length > 65535 || file.bytes.length > 0xffffffff || /(^|\/)\.\.(\/|$)|^[/\\]/.test(file.name)) throw new RangeError('Invalid ZIP entry')
    localSize += 30 + file.nameBytes.length + file.bytes.length
    centralSize += 46 + file.nameBytes.length
  }
  if (localSize + centralSize + 22 > 0xffffffff) throw new RangeError('ZIP32 size limit exceeded')
  const output = new Uint8Array(localSize + centralSize + 22)
  const view = new DataView(output.buffer)
  let offset = 0, directory = localSize
  const u16 = (at: number, value: number) => view.setUint16(at, value, true)
  const u32 = (at: number, value: number) => view.setUint32(at, value, true)
  for (const file of entries) {
    const { nameBytes, bytes, crc } = file
    u32(offset, 0x04034b50); u16(offset + 4, 20); u16(offset + 6, 0x0800); u16(offset + 12, 33)
    u32(offset + 14, crc); u32(offset + 18, bytes.length); u32(offset + 22, bytes.length); u16(offset + 26, nameBytes.length)
    output.set(nameBytes, offset + 30); output.set(bytes, offset + 30 + nameBytes.length)
    u32(directory, 0x02014b50); u16(directory + 4, 20); u16(directory + 6, 20); u16(directory + 8, 0x0800); u16(directory + 14, 33)
    u32(directory + 16, crc); u32(directory + 20, bytes.length); u32(directory + 24, bytes.length); u16(directory + 28, nameBytes.length); u32(directory + 42, offset)
    output.set(nameBytes, directory + 46)
    offset += 30 + nameBytes.length + bytes.length; directory += 46 + nameBytes.length
  }
  u32(directory, 0x06054b50); u16(directory + 8, files.length); u16(directory + 10, files.length)
  u32(directory + 12, centralSize); u32(directory + 16, localSize)
  return output
}
