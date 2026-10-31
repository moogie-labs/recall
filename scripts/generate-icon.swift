// Reproducible vector source for Recall's 1024px app icon.
// Run from the repository root: swift scripts/generate-icon.swift
import AppKit

let size = 1024
let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
    bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
    colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
NSColor(calibratedRed: 0.10, green: 0.25, blue: 0.29, alpha: 1).setFill()
NSBezierPath(roundedRect: NSRect(x: 48, y: 48, width: 928, height: 928),
    xRadius: 208, yRadius: 208).fill()

// A geometric R, drawn as paths rather than relying on an installed font.
let letter = NSBezierPath()
letter.windingRule = .evenOdd
letter.move(to: NSPoint(x: 300, y: 240))
letter.line(to: NSPoint(x: 300, y: 784))
letter.line(to: NSPoint(x: 530, y: 784))
letter.curve(to: NSPoint(x: 720, y: 610), controlPoint1: NSPoint(x: 655, y: 784), controlPoint2: NSPoint(x: 720, y: 720))
letter.curve(to: NSPoint(x: 604, y: 452), controlPoint1: NSPoint(x: 720, y: 529), controlPoint2: NSPoint(x: 680, y: 476))
letter.line(to: NSPoint(x: 756, y: 240))
letter.line(to: NSPoint(x: 612, y: 240))
letter.line(to: NSPoint(x: 478, y: 434))
letter.line(to: NSPoint(x: 424, y: 434))
letter.line(to: NSPoint(x: 424, y: 240))
letter.close()
letter.move(to: NSPoint(x: 424, y: 674))
letter.line(to: NSPoint(x: 424, y: 544))
letter.line(to: NSPoint(x: 522, y: 544))
letter.curve(to: NSPoint(x: 592, y: 609), controlPoint1: NSPoint(x: 570, y: 544), controlPoint2: NSPoint(x: 592, y: 564))
letter.curve(to: NSPoint(x: 522, y: 674), controlPoint1: NSPoint(x: 592, y: 654), controlPoint2: NSPoint(x: 570, y: 674))
letter.close()
NSColor(calibratedRed: 0.96, green: 0.94, blue: 0.85, alpha: 1).setFill()
letter.fill()
NSGraphicsContext.restoreGraphicsState()
try bitmap.representation(using: .png, properties: [:])!
    .write(to: URL(fileURLWithPath: "desktop/build/appicon.png"))
