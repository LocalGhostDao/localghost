import AVFoundation
import SwiftUI
import UIKit
import VisionKit

/// Whether the camera can be used, asked of AVFoundation rather than inferred from VisionKit's
/// isAvailable (which is false both before the question was asked and after a refusal).
enum CameraAccess: Equatable {
  case unknown
  case granted
  case refused
  case unsupported

  static func current(supported: Bool = DataScannerViewController.isSupported) -> CameraAccess {
    guard supported else { return .unsupported }
    switch AVCaptureDevice.authorizationStatus(for: .video) {
    case .authorized: return .granted
    case .notDetermined: return .unknown
    default: return .refused // denied, or restricted by Screen Time or a profile
    }
  }
}

struct QRScannerSheet: View {
  @Environment(\.dismiss) private var dismiss
  @Environment(\.scenePhase) private var scenePhase
  @Environment(\.openURL) private var openURL
  @Environment(GhostTheme.self) private var theme
  @State private var scannerError: String?
  @State private var access = CameraAccess.current()

  let onPayload: (String) -> Bool

  var body: some View {
    cameraScreen
    .preferredColorScheme(.dark)
    .task {
      // the first time: ask, and the scanner starts when the answer is yes
      if access == .unknown {
        _ = await AVCaptureDevice.requestAccess(for: .video)
        access = CameraAccess.current()
      }
    }
    .onChange(of: scenePhase) { _, phase in
      // back from Settings with the camera allowed: the scanner starts by itself
      if phase == .active {
        access = CameraAccess.current()
      }
    }
  }

  private var accessWords: String? {
    switch access {
    case .refused:
      "the camera is off for LocalGhost: allow it in Settings to scan the box's QR"
    case .unsupported:
      "this iPhone cannot read codes with its camera"
    case .unknown, .granted:
      nil
    }
  }

  private var cameraScreen: some View {
    ZStack {
      theme.background.ignoresSafeArea()

      if access == .granted && DataScannerViewController.isAvailable {
        QRScannerView(
          onPayload: handlePayload,
          onError: { scannerError = $0.localizedDescription }
        )
        .ignoresSafeArea()
      }

      if access == .granted || access == .unknown {
        ScannerReticle()
      }

      VStack(spacing: 0) {
        Text("> SCAN THE BOX QR")
          .font(theme.font(.callout))
          .foregroundStyle(theme.accentDim)
          .padding(.horizontal, 16)
          .padding(.vertical, 10)
          .background(theme.background.opacity(0.78), in: RoundedRectangle(cornerRadius: 12))
          .accessibilityAddTraits(.isHeader)

        Spacer()

        VStack(spacing: 12) {
          VStack(spacing: 8) {
            Text("> point at the QR on the box")
              .font(theme.font(.callout))
              .foregroundStyle(theme.accent)

            if let refusal = accessWords {
              Text("! \(refusal)")
                .font(theme.font(.callout))
                .foregroundStyle(theme.warning)
                .multilineTextAlignment(.center)
            } else if let scannerError {
              Text("! \(scannerError)")
                .font(theme.font(.callout))
                .foregroundStyle(theme.warning)
                .multilineTextAlignment(.center)
            }
          }
          .frame(maxWidth: .infinity)
          .padding(14)
          .background(theme.background.opacity(0.82), in: RoundedRectangle(cornerRadius: 12))

          if access == .refused, let settings = URL(string: UIApplication.openSettingsURLString) {
            GhostButton(title: "Open settings") {
              openURL(settings)
            }
            .accessibilityIdentifier("scanner.settings")
          }

          GhostButton(title: "Cancel") {
            dismiss()
          }
          .accessibilityIdentifier("scanner.cancel")
        }
      }
      .padding(.horizontal, 24)
      .padding(.top, 20)
      .padding(.bottom, 16)
    }
  }

  @discardableResult
  private func handlePayload(_ payload: String) -> Bool {
    if onPayload(payload) {
      dismiss()
      return true
    } else {
      scannerError = "that code is not a usable LocalGhost enrolment link"
      return false
    }
  }
}

private struct ScannerReticle: View {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.accessibilityReduceMotion) private var reduceMotion
  @State private var animationStartedAt = Date()

  private let scanTickRadius: CGFloat = 104
  private let scanTickDegreesPerSecond = 3.2 / 0.060

  var body: some View {
    TimelineView(.animation(minimumInterval: 0.060, paused: reduceMotion || theme.preferences.reduceMotion)) { timeline in
      ZStack {
        ForEach(0..<4, id: \.self) { index in
          Circle()
            .trim(from: 0.03, to: 0.19)
            .stroke(theme.accentDim, style: StrokeStyle(lineWidth: 6, lineCap: .round))
            .rotationEffect(.degrees(Double(index) * 90))
        }

        ZStack {
          HStack(spacing: 14) {
            Rectangle().frame(width: 16, height: 2)
            Rectangle().frame(width: 16, height: 2)
          }
          VStack(spacing: 14) {
            Rectangle().frame(width: 2, height: 16)
            Rectangle().frame(width: 2, height: 16)
          }
        }
        .foregroundStyle(theme.accent)

        Circle()
          .fill(theme.accent)
          .frame(width: 12, height: 12)
          .offset(scanTickOffset(at: timeline.date))
      }
    }
    .frame(width: 230, height: 230)
    .allowsHitTesting(false)
    .accessibilityHidden(true)
  }

  private func scanTickOffset(at date: Date) -> CGSize {
    guard !(reduceMotion || theme.preferences.reduceMotion) else { return CGSize(width: 0, height: scanTickRadius) }

    let elapsed = max(0, date.timeIntervalSince(animationStartedAt))
    let angle = (-90 + elapsed * scanTickDegreesPerSecond) * .pi / 180
    return CGSize(
      width: scanTickRadius * cos(angle),
      height: scanTickRadius * sin(angle)
    )
  }
}

private struct QRScannerView: UIViewControllerRepresentable {
  let onPayload: (String) -> Bool
  let onError: (Error) -> Void

  func makeCoordinator() -> Coordinator {
    Coordinator(onPayload: onPayload, onError: onError)
  }

  func makeUIViewController(context: Context) -> DataScannerViewController {
    let controller = DataScannerViewController(
      recognizedDataTypes: [.barcode(symbologies: [.qr])],
      qualityLevel: .balanced,
      recognizesMultipleItems: false,
      isHighFrameRateTrackingEnabled: false,
      isPinchToZoomEnabled: true,
      isGuidanceEnabled: false,
      isHighlightingEnabled: false
    )
    controller.delegate = context.coordinator
    return controller
  }

  func updateUIViewController(_ controller: DataScannerViewController, context _: Context) {
    guard !controller.isScanning else { return }
    do {
      try controller.startScanning()
    } catch {
      onError(error)
    }
  }

  static func dismantleUIViewController(
    _ controller: DataScannerViewController,
    coordinator _: Coordinator
  ) {
    controller.stopScanning()
  }

  @MainActor
  final class Coordinator: NSObject, DataScannerViewControllerDelegate {
    private let onPayload: (String) -> Bool
    private let onError: (Error) -> Void
    private var streamAssembler = EnrolmentQRStreamAssembler()
    private var emittedPayload = false
    private var restartTask: Task<Void, Never>?

    init(onPayload: @escaping (String) -> Bool, onError: @escaping (Error) -> Void) {
      self.onPayload = onPayload
      self.onError = onError
    }

    func dataScanner(
      _ dataScanner: DataScannerViewController,
      didAdd addedItems: [RecognizedItem],
      allItems _: [RecognizedItem]
    ) {
      trace("didAdd items=\(addedItems.count)")
      consume(addedItems, from: dataScanner)
    }

    func dataScanner(
      _ dataScanner: DataScannerViewController,
      didUpdate updatedItems: [RecognizedItem],
      allItems _: [RecognizedItem]
    ) {
      // The box rotates frames without moving the QR. VisionKit therefore normally updates the
      // existing recognised item rather than adding a new one; both callbacks are part of the scan.
      trace("didUpdate items=\(updatedItems.count)")
      consume(updatedItems, from: dataScanner)
    }

    private func consume(
      _ items: [RecognizedItem],
      from dataScanner: DataScannerViewController
    ) {
      guard !emittedPayload else { return }
      for item in items {
        guard case .barcode(let barcode) = item else { continue }
        let payload = barcode.payloadStringValue
          ?? barcode.observation.payloadData
            .flatMap(QRByteModePayload.decode)
            .flatMap { String(data: $0, encoding: .isoLatin1) }
        guard let payload else {
          trace("barcode payload unavailable as text or byte mode; recycling scanner")
          restartScanning(dataScanner)
          return
        }

        let completePayload: String
        if streamAssembler.isFrame(payload) {
          guard let rebuilt = streamAssembler.offer(payload),
                let value = String(data: rebuilt, encoding: .isoLatin1)
          else {
            // A valid frame is progress, not an invalid enrolment link. Keep scanning until any K
            // distinct frames reconstruct the link and its full CRC verifies. VisionKit tracks a
            // barcode by position and otherwise keeps returning the first terminal frame forever,
            // so restart recognition between rotations while leaving the camera preview in place.
            let progress = streamAssembler.progress
            trace("LGQR2 progress=\(progress.captured)/\(progress.required)")
            restartScanning(dataScanner)
            return
          }
          trace("LGQR2 payload rebuilt and CRC verified")
          completePayload = value
        } else {
          trace("non-LGQR2 payload length=\(payload.count) enrolment=\(payload.hasPrefix("localghost://"))")
          completePayload = payload
        }

        if onPayload(completePayload) {
          trace("enrolment payload accepted")
          emittedPayload = true
          restartTask?.cancel()
          dataScanner.stopScanning()
        } else {
          trace("complete payload rejected")
          restartScanning(dataScanner)
        }
        return
      }
    }

    private func restartScanning(_ dataScanner: DataScannerViewController) {
      restartTask?.cancel()
      dataScanner.stopScanning()
      restartTask = Task { @MainActor [weak self, weak dataScanner] in
        try? await Task.sleep(for: .milliseconds(180))
        guard let self, let dataScanner, !self.emittedPayload, !Task.isCancelled else { return }
        do {
          try dataScanner.startScanning()
          self.trace("scanner restarted")
        } catch {
          self.onError(error)
        }
      }
    }

    private func trace(_ message: String) {
      #if DEBUG
      print("[LocalGhost QR] \(message)")
      #endif
    }

    func dataScanner(
      _: DataScannerViewController,
      becameUnavailableWithError error: DataScannerViewController.ScanningUnavailable
    ) {
      onError(error)
    }
  }
}

/// Reassembles the box's LGQR2 rotating enrolment stream. The wire contract mirrors
/// `server/internal/pair/qrstream.go` and Android's `StreamAssembler`: any K distinct data/parity
/// frames recover the payload through a Cauchy matrix over GF(256).
struct EnrolmentQRStreamAssembler {
  private struct Identity: Hashable {
    let dataFrameCount: Int
    let parityFrameCount: Int
    let payloadLength: Int
    let payloadCRC: UInt32
  }

  private var partsByIdentity: [Identity: [Int: Data]] = [:]
  private var votesByIdentity: [Identity: Int] = [:]

  func isFrame(_ value: String) -> Bool {
    value.hasPrefix("LGQR2 ")
  }

  var progress: (captured: Int, required: Int) {
    guard let identity = votesByIdentity.max(by: { $0.value < $1.value })?.key else {
      return (0, 0)
    }
    return (min(partsByIdentity[identity]?.count ?? 0, identity.dataFrameCount), identity.dataFrameCount)
  }

  mutating func offer(_ value: String) -> Data? {
    let fields = value.split(
      separator: " ",
      maxSplits: 7,
      omittingEmptySubsequences: false
    )
    guard fields.count == 8,
          fields[0] == "LGQR2",
          let index = Int(fields[1]),
          let dataFrameCount = Int(fields[2]),
          let parityFrameCount = Int(fields[3]),
          let payloadLength = Int(fields[4]),
          let payloadCRC = UInt32(fields[5], radix: 16),
          let frameCRC = UInt32(fields[6], radix: 16),
          dataFrameCount > 0,
          parityFrameCount >= 0,
          payloadLength > 0,
          dataFrameCount + parityFrameCount <= 255,
          index >= 0,
          index < dataFrameCount + parityFrameCount,
          let body = String(fields[7]).data(using: .isoLatin1)
    else {
      return nil
    }

    let blockSize = (payloadLength + dataFrameCount - 1) / dataFrameCount
    guard body.count == blockSize,
          QRCRC32.checksum(body) & 0xffff == frameCRC
    else {
      return nil
    }

    let identity = Identity(
      dataFrameCount: dataFrameCount,
      parityFrameCount: parityFrameCount,
      payloadLength: payloadLength,
      payloadCRC: payloadCRC
    )
    votesByIdentity[identity, default: 0] += 1
    partsByIdentity[identity, default: [:]][index] = body

    guard let winningIdentity = votesByIdentity.max(by: { $0.value < $1.value })?.key,
          let parts = partsByIdentity[winningIdentity],
          parts.count >= winningIdentity.dataFrameCount,
          let payload = solve(winningIdentity, parts: parts)
    else {
      return nil
    }

    guard QRCRC32.checksum(payload) == winningIdentity.payloadCRC else {
      // One frame got through its truncated frame CRC. Discard this set rather than completing with
      // poisoned credentials; subsequent rotations rebuild it from clean frames.
      partsByIdentity.removeValue(forKey: winningIdentity)
      votesByIdentity.removeValue(forKey: winningIdentity)
      return nil
    }
    return payload
  }

  private func solve(_ identity: Identity, parts: [Int: Data]) -> Data? {
    let k = identity.dataFrameCount
    let blockSize = (identity.payloadLength + k - 1) / k
    var rows: [[UInt8]] = []
    var rightHandSides: [[UInt8]] = []

    for index in 0..<(k + identity.parityFrameCount) {
      guard let body = parts[index] else { continue }
      var row = [UInt8](repeating: 0, count: k)
      if index < k {
        row[index] = 1
      } else {
        for column in 0..<k {
          row[column] = GaloisField256.inverse(UInt8((k + index - k) ^ column))
        }
      }
      rows.append(row)
      rightHandSides.append(Array(body))
      if rows.count == k { break }
    }
    guard rows.count == k else { return nil }

    for column in 0..<k {
      guard let pivot = (column..<k).first(where: { rows[$0][column] != 0 }) else {
        return nil
      }
      rows.swapAt(column, pivot)
      rightHandSides.swapAt(column, pivot)

      let inverse = GaloisField256.inverse(rows[column][column])
      for index in 0..<k {
        rows[column][index] = GaloisField256.multiply(rows[column][index], inverse)
      }
      for index in 0..<blockSize {
        rightHandSides[column][index] = GaloisField256.multiply(
          rightHandSides[column][index], inverse
        )
      }

      for row in 0..<k where row != column && rows[row][column] != 0 {
        let factor = rows[row][column]
        for index in 0..<k {
          rows[row][index] ^= GaloisField256.multiply(factor, rows[column][index])
        }
        for index in 0..<blockSize {
          rightHandSides[row][index] ^= GaloisField256.multiply(
            factor, rightHandSides[column][index]
          )
        }
      }
    }

    return Data(rightHandSides.flatMap { $0 }.prefix(identity.payloadLength))
  }
}

private enum GaloisField256 {
  private static let tables: (exponents: [UInt8], logarithms: [Int]) = {
    var exponents = [UInt8](repeating: 0, count: 512)
    var logarithms = [Int](repeating: 0, count: 256)
    var value = 1
    for index in 0..<255 {
      exponents[index] = UInt8(value)
      logarithms[value] = index
      value <<= 1
      if value & 0x100 != 0 { value ^= 0x11d }
    }
    for index in 255..<512 {
      exponents[index] = exponents[index - 255]
    }
    return (exponents, logarithms)
  }()

  static func multiply(_ lhs: UInt8, _ rhs: UInt8) -> UInt8 {
    guard lhs != 0, rhs != 0 else { return 0 }
    return tables.exponents[tables.logarithms[Int(lhs)] + tables.logarithms[Int(rhs)]]
  }

  static func inverse(_ value: UInt8) -> UInt8 {
    tables.exponents[255 - tables.logarithms[Int(value)]]
  }
}

private enum QRCRC32 {
  private static let table: [UInt32] = (0..<256).map { value in
    var checksum = UInt32(value)
    for _ in 0..<8 {
      checksum = checksum & 1 == 1
        ? 0xedb88320 ^ (checksum >> 1)
        : checksum >> 1
    }
    return checksum
  }

  static func checksum(_ data: Data) -> UInt32 {
    var checksum = UInt32.max
    for byte in data {
      checksum = table[Int((checksum ^ UInt32(byte)) & 0xff)] ^ (checksum >> 8)
    }
    return checksum ^ UInt32.max
  }
}

/// Vision exposes arbitrary-byte QR segments through `VNBarcodeObservation.payloadData`, but that
/// value is the QR data-codeword stream (mode + character count + bytes + padding), not merely the
/// segment bytes. Current box frames are versions 1...8 at EC level M, hence an 8-bit byte-mode
/// count. The 16-bit branch keeps the decoder correct if the box later emits version 10+ symbols.
enum QRByteModePayload {
  static func decode(_ codewords: Data) -> Data? {
    var reader = BitReader(bytes: Array(codewords))
    guard reader.read(4) == 0b0100 else { return nil }

    // At level M the server's versions 1...9 carry at most 182 data codewords. Version 10+ uses a
    // 16-bit byte count under ISO/IEC 18004.
    let countBits = codewords.count <= 182 ? 8 : 16
    guard let byteCount = reader.read(countBits),
          byteCount > 0,
          byteCount <= reader.remainingBits / 8
    else {
      return nil
    }

    var payload = Data(capacity: byteCount)
    for _ in 0..<byteCount {
      guard let byte = reader.read(8) else { return nil }
      payload.append(UInt8(byte))
    }
    return payload
  }

  private struct BitReader {
    let bytes: [UInt8]
    var offset = 0

    var remainingBits: Int { bytes.count * 8 - offset }

    mutating func read(_ count: Int) -> Int? {
      guard count >= 0, count <= remainingBits else { return nil }
      var value = 0
      for _ in 0..<count {
        let byte = bytes[offset / 8]
        let bit = (byte >> UInt8(7 - offset % 8)) & 1
        value = value << 1 | Int(bit)
        offset += 1
      }
      return value
    }
  }
}
