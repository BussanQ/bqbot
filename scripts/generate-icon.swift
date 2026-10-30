// Regenerate the app icon on macOS: swift scripts/generate-icon.swift resources/icon.png
import AppKit

let size = 1024
let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
  bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
  colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
let context = NSGraphicsContext(bitmapImageRep: bitmap)!
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
context.imageInterpolation = .high
context.cgContext.setShouldAntialias(true)

func color(_ hex: UInt32) -> NSColor {
  NSColor(srgbRed: CGFloat((hex >> 16) & 255) / 255,
    green: CGFloat((hex >> 8) & 255) / 255,
    blue: CGFloat(hex & 255) / 255, alpha: 1)
}

// Transparent padding keeps the silhouette aligned with other macOS Dock icons.
let plate = NSBezierPath(roundedRect: NSRect(x: 100, y: 100, width: 824, height: 824),
  xRadius: 184, yRadius: 184)
NSGraphicsContext.saveGraphicsState()
let shadow = NSShadow()
shadow.shadowColor = NSColor.black.withAlphaComponent(0.18)
shadow.shadowBlurRadius = 24
shadow.shadowOffset = NSSize(width: 0, height: -12)
shadow.set()
color(0xEAECE3).setFill()
plate.fill()
NSGraphicsContext.restoreGraphicsState()
NSGradient(starting: color(0xE6E9DE), ending: color(0xFCFBF5))!.draw(in: plate, angle: 90)
NSGraphicsContext.saveGraphicsState()
plate.addClip()
let rim = NSBezierPath(roundedRect: NSRect(x: 102, y: 102, width: 820, height: 820),
  xRadius: 182, yRadius: 182)
NSColor.white.withAlphaComponent(0.65).setStroke()
rim.lineWidth = 3
rim.stroke()
NSGraphicsContext.restoreGraphicsState()

// Four softly rounded tiles echo the workspace's existing bqbot brand mark.
NSGraphicsContext.saveGraphicsState()
let transform = AffineTransform(translationByX: 512, byY: 512)
(transform as NSAffineTransform).concat()
let rotation = NSAffineTransform()
rotation.rotate(byDegrees: 7)
rotation.concat()
let tiles: [(CGFloat, CGFloat, UInt32, UInt32)] = [
  (-246, 22, 0x3D5948, 0x57745D),
  (22, 22, 0x9CAE91, 0xB7C8AA),
  (-246, -246, 0x748C70, 0x91A686),
  (22, -246, 0x3D5948, 0x57745D),
]
for (x, y, bottom, top) in tiles {
  let tile = NSBezierPath(roundedRect: NSRect(x: x, y: y, width: 224, height: 224),
    xRadius: 66, yRadius: 66)
  NSGraphicsContext.saveGraphicsState()
  let relief = NSShadow()
  relief.shadowColor = color(0x263E30).withAlphaComponent(0.16)
  relief.shadowBlurRadius = 10
  relief.shadowOffset = NSSize(width: 0, height: -5)
  relief.set()
  color(bottom).setFill()
  tile.fill()
  NSGraphicsContext.restoreGraphicsState()
  NSGradient(starting: color(bottom), ending: color(top))!.draw(in: tile, angle: 90)
}
NSGraphicsContext.restoreGraphicsState()
NSGraphicsContext.restoreGraphicsState()

let output = CommandLine.arguments.dropFirst().first ?? "resources/icon.png"
let data = bitmap.representation(using: .png, properties: [:])!
try data.write(to: URL(fileURLWithPath: output), options: .atomic)
print("Generated \(output) (\(size) × \(size))")
