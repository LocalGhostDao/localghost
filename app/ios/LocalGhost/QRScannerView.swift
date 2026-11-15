import SwiftUI
import VisionKit

struct QRScannerSheet: View {
  @Environment(\.dismiss) private var dismiss
  @Environment(GhostTheme.self) private var theme
  @State private var scannerError: String?

  let onPayload: (String) -> Bool

  var body: some View {
    cameraScreen
    .preferredColorScheme(.dark)
  }

  private var cameraScreen: some View {
    ZStack {
      theme.background.ignoresSafeArea()

      if DataScannerViewController.isSupported && DataScannerViewController.isAvailable {
        QRScannerView(
          onPayload: handlePayload,
          onError: { scannerError = $0.localizedDescription }
        )
        .ignoresSafeArea()
      }

      ScannerReticle()

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

            if let scannerError {
              Text("! \(scannerError)")
                .font(theme.font(.callout))
                .foregroundStyle(theme.warning)
                .multilineTextAlignment(.center)
            }
          }
          .frame(maxWidth: .infinity)
          .padding(14)
          .background(theme.background.opacity(0.82), in: RoundedRectangle(cornerRadius: 12))

          GhostButton(title: "Cancel / Type Instead") {
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
    TimelineView(.animation(minimumInterval: 0.060, paused: reduceMotion)) { timeline in
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
    guard !reduceMotion else { return CGSize(width: 0, height: scanTickRadius) }

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
    private var emittedPayload = false

    init(onPayload: @escaping (String) -> Bool, onError: @escaping (Error) -> Void) {
      self.onPayload = onPayload
      self.onError = onError
    }

    func dataScanner(
      _ dataScanner: DataScannerViewController,
      didAdd addedItems: [RecognizedItem],
      allItems _: [RecognizedItem]
    ) {
      guard !emittedPayload else { return }
      for item in addedItems {
        guard case .barcode(let barcode) = item,
              let payload = barcode.payloadStringValue
        else {
          continue
        }
        if onPayload(payload) {
          emittedPayload = true
          dataScanner.stopScanning()
        }
        return
      }
    }

    func dataScanner(
      _: DataScannerViewController,
      becameUnavailableWithError error: DataScannerViewController.ScanningUnavailable
    ) {
      onError(error)
    }
  }
}
