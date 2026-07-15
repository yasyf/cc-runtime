import AVFoundation
import CcRuntimeKit
import SwiftUI
import VisionKit

/// PairScannerView scans a pairing QR code with VisionKit, decoding the recognized
/// string into a PairPayload through the shared PairingModel. Camera denial or an
/// unsupported device falls back to pasting the payload by hand.
struct PairScannerView: View {
    let pairing: PairingModel

    @State private var cameraAuthorized: Bool?
    @State private var scanToken = 0
    @Environment(\.openURL) private var openURL

    var body: some View {
        content
            .navigationTitle("Scan QR Code")
            .navigationBarTitleDisplayMode(.inline)
            .task {
                if cameraAuthorized == nil {
                    cameraAuthorized = await Self.resolveCameraAccess()
                }
            }
            .alert("Couldn't Pair", isPresented: failureBinding) {
                Button("Scan Again") {
                    pairing.reset()
                    scanToken += 1
                }
            } message: {
                Text(failureMessage)
            }
    }

    @ViewBuilder
    private var content: some View {
        switch cameraAuthorized {
        case .none:
            ProgressView("Preparing camera…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .some(false):
            fallback(
                title: "Camera Access Needed",
                message: "Allow camera access to scan a pairing QR code, or paste the payload by hand.",
                showSettings: true
            )
        case .some(true):
            if scannerAvailable {
                scanner
            } else {
                fallback(
                    title: "Scanning Unavailable",
                    message: "This device can't scan QR codes. Paste the pairing payload instead.",
                    showSettings: false
                )
            }
        }
    }

    private var scanner: some View {
        ZStack(alignment: .bottom) {
            DataScannerRepresentable(onScan: { pairing.pair(scanned: $0) })
                .id(scanToken)
                .ignoresSafeArea()
            VStack(spacing: 12) {
                Text("Point the camera at the pairing QR code.")
                    .font(.subheadline)
                    .padding(10)
                    .background(.ultraThinMaterial, in: Capsule())
                NavigationLink {
                    PastePairView(pairing: pairing)
                } label: {
                    Label("Paste Payload Instead", systemImage: "doc.on.clipboard")
                }
                .buttonStyle(.borderedProminent)
            }
            .padding(.bottom, 32)
        }
    }

    private func fallback(title: String, message: String, showSettings: Bool) -> some View {
        ContentUnavailableView {
            Label(title, systemImage: "camera.metering.unknown")
        } description: {
            Text(message)
        } actions: {
            NavigationLink {
                PastePairView(pairing: pairing)
            } label: {
                Text("Paste Payload")
            }
            .buttonStyle(.borderedProminent)
            if showSettings {
                Button("Open Settings") {
                    if let url = URL(string: UIApplication.openSettingsURLString) {
                        openURL(url)
                    }
                }
            }
        }
    }

    private var scannerAvailable: Bool {
        DataScannerViewController.isSupported && DataScannerViewController.isAvailable
    }

    private var failureBinding: Binding<Bool> {
        Binding(
            get: {
                if case .failed = pairing.phase {
                    true
                } else {
                    false
                }
            },
            set: { presented in
                if !presented {
                    pairing.reset()
                }
            }
        )
    }

    private var failureMessage: String {
        if case let .failed(message) = pairing.phase {
            return message
        }
        return ""
    }

    private static func resolveCameraAccess() async -> Bool {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            true
        case .notDetermined:
            await AVCaptureDevice.requestAccess(for: .video)
        default:
            false
        }
    }
}

/// DataScannerRepresentable hosts a VisionKit DataScannerViewController restricted to
/// QR codes, reporting the first recognized payload string to `onScan`.
private struct DataScannerRepresentable: UIViewControllerRepresentable {
    let onScan: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(
            recognizedDataTypes: [.barcode(symbologies: [.qr])],
            qualityLevel: .balanced,
            recognizesMultipleItems: false,
            isHighFrameRateTrackingEnabled: false,
            isHighlightingEnabled: true
        )
        scanner.delegate = context.coordinator
        return scanner
    }

    func updateUIViewController(_ uiViewController: DataScannerViewController, context _: Context) {
        try? uiViewController.startScanning()
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(onScan: onScan)
    }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        private let onScan: (String) -> Void
        private var handled = false

        init(onScan: @escaping (String) -> Void) {
            self.onScan = onScan
        }

        func dataScanner(
            _ dataScanner: DataScannerViewController,
            didAdd addedItems: [RecognizedItem],
            allItems _: [RecognizedItem]
        ) {
            guard !handled else { return }
            for case let .barcode(barcode) in addedItems {
                guard let value = barcode.payloadStringValue else { continue }
                handled = true
                dataScanner.stopScanning()
                onScan(value)
                return
            }
        }
    }
}
