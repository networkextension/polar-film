import CoreGraphics
import Foundation
import ImageIO

// PHash.swift — client-side perceptual hash (dHash), a faithful port of the
// server's phash.go so screenshots uploaded via the direct-to-storage path
// carry the SAME hash the old multipart path computed (cross-dedup must hold).
//
// Algorithm (identical to phash.go):
//   • decode the image; nearest-neighbour sample a 9×8 grid at
//     sx = x*W/9, sy = y*H/8 (integer division, top-left origin);
//   • grayscale each sample with 16-bit luma (r*299 + g*587 + b*114)/1000,
//     where the 8-bit channel c is widened to 16-bit as c*257 (matches Go's
//     color.RGBA() returning premultiplied 16-bit values for opaque pixels);
//   • emit 64 bits from adjacent-pixel brightness comparisons (gray[x] <
//     gray[x+1]) across 8 columns × 8 rows;
//   • format as 16 lowercase hex chars. "" if the image can't be decoded.
func perceptualDHash(_ data: Data) -> String {
    guard let src = CGImageSourceCreateWithData(data as CFData, nil),
          let img = CGImageSourceCreateImageAtIndex(src, 0, nil) else {
        return ""
    }
    let w = img.width
    let h = img.height
    if w == 0 || h == 0 { return "" }

    // Render to a normalized top-down RGBA8 buffer so pixel (x,y) with y=0 is
    // the TOP row, matching Go's image coordinate space.
    let bytesPerRow = w * 4
    var buf = [UInt8](repeating: 0, count: bytesPerRow * h)
    let cs = CGColorSpaceCreateDeviceRGB()
    let bmInfo = CGImageAlphaInfo.premultipliedLast.rawValue | CGBitmapInfo.byteOrder32Big.rawValue
    let drawn: Bool = buf.withUnsafeMutableBytes { raw -> Bool in
        guard let ctx = CGContext(data: raw.baseAddress, width: w, height: h,
                                  bitsPerComponent: 8, bytesPerRow: bytesPerRow,
                                  space: cs, bitmapInfo: bmInfo) else { return false }
        // Flip so buffer row 0 = top of the image (CGContext is bottom-left).
        ctx.translateBy(x: 0, y: CGFloat(h))
        ctx.scaleBy(x: 1, y: -1)
        ctx.draw(img, in: CGRect(x: 0, y: 0, width: CGFloat(w), height: CGFloat(h)))
        return true
    }
    if !drawn { return "" }

    let gw = 9, gh = 8
    var gray = [[UInt32]](repeating: [UInt32](repeating: 0, count: gw), count: gh)
    for y in 0..<gh {
        for x in 0..<gw {
            let sx = x * w / gw
            let sy = y * h / gh
            let o = sy * bytesPerRow + sx * 4
            let r = UInt32(buf[o]) * 257
            let g = UInt32(buf[o + 1]) * 257
            let b = UInt32(buf[o + 2]) * 257
            gray[y][x] = (r * 299 + g * 587 + b * 114) / 1000
        }
    }

    var bits: UInt64 = 0
    var n: UInt64 = 0
    for y in 0..<gh {
        for x in 0..<(gw - 1) {
            if gray[y][x] < gray[y][x + 1] { bits |= (1 << n) }
            n += 1
        }
    }
    return String(format: "%016x", bits)
}
