import Observation
import SwiftUI
import UIKit

@MainActor
@Observable
final class GhostTheme {
  let background = Color(red: 17 / 255, green: 17 / 255, blue: 17 / 255)
  let surface = Color(red: 26 / 255, green: 26 / 255, blue: 26 / 255)
  let accent = Color(red: 51 / 255, green: 1, blue: 0)
  let accentDim = Color(red: 26 / 255, green: 128 / 255, blue: 0)
  let text = Color(red: 224 / 255, green: 224 / 255, blue: 224 / 255)
  let textDim = Color(red: 160 / 255, green: 160 / 255, blue: 160 / 255)
  let border = Color(red: 68 / 255, green: 68 / 255, blue: 68 / 255)
  let warning = Color(red: 1, green: 138 / 255, blue: 138 / 255)

  func font(_ style: Font.TextStyle, weight: Font.Weight = .regular) -> Font {
    .system(style, design: .monospaced).weight(weight)
  }
}

struct GhostScreen<Content: View>: View {
  @Environment(GhostTheme.self) private var theme

  @ViewBuilder let content: Content

  var body: some View {
    ZStack {
      theme.background.ignoresSafeArea()
      content
    }
    .foregroundStyle(theme.text)
  }
}

struct TerminalSectionLabel: View {
  @Environment(GhostTheme.self) private var theme

  let text: String

  var body: some View {
    Text("> \(text.uppercased())")
      .font(theme.font(.subheadline))
      .foregroundStyle(theme.accentDim)
      .frame(maxWidth: .infinity, alignment: .leading)
      .accessibilityAddTraits(.isHeader)
  }
}

struct GhostButton: View {
  let title: String
  let action: () -> Void

  var body: some View {
    Button(action: action) {
      Text("[ \(title.uppercased()) ]")
        .frame(maxWidth: .infinity)
    }
    .buttonStyle(GhostButtonStyle())
    .accessibilityLabel(title)
  }
}

private struct GhostButtonStyle: ButtonStyle {
  @Environment(GhostTheme.self) private var theme
  @Environment(\.isEnabled) private var isEnabled
  @Environment(\.accessibilityReduceMotion) private var reduceMotion

  func makeBody(configuration: Configuration) -> some View {
    configuration.label
      .font(theme.font(.body))
      .lineLimit(1)
      .minimumScaleFactor(0.72)
      .foregroundStyle(isEnabled ? theme.accent : theme.border)
      .padding(.horizontal, 10)
      .frame(minHeight: 54)
      .background(configuration.isPressed && isEnabled ? theme.surface : theme.background)
      .overlay {
        Rectangle()
          .stroke(isEnabled ? theme.accent : theme.border, lineWidth: 1)
      }
      .contentShape(Rectangle())
      .scaleEffect(configuration.isPressed && !reduceMotion ? 0.985 : 1)
      .animation(
        reduceMotion ? nil : .easeOut(duration: 0.12),
        value: configuration.isPressed
      )
  }
}

struct GhostTextField: View {
  @Environment(GhostTheme.self) private var theme

  let label: String
  let placeholder: String
  @Binding var text: String
  var keyboardType: UIKeyboardType = .default
  var textContentType: UITextContentType?

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      Text(label.uppercased())
        .font(theme.font(.caption))
        .foregroundStyle(theme.accentDim)

      TextField(
        "",
        text: $text,
        prompt: Text(placeholder).foregroundStyle(theme.textDim)
      )
      .font(theme.font(.body))
      .foregroundStyle(theme.text)
      .tint(theme.accent)
      .keyboardType(keyboardType)
      .textContentType(textContentType)
      .textInputAutocapitalization(.never)
      .autocorrectionDisabled()
      .padding(.horizontal, 16)
      .frame(minHeight: 58)
      .background(theme.background)
      .overlay {
        RoundedRectangle(cornerRadius: 12)
          .stroke(theme.border, lineWidth: 1)
      }
    }
  }
}

struct GhostReadOnlyField: View {
  @Environment(GhostTheme.self) private var theme

  let label: String
  let value: String
  let placeholder: String

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      Text(label.uppercased())
        .font(theme.font(.caption))
        .foregroundStyle(theme.accentDim)

      Text(value.isEmpty ? placeholder : value)
        .font(theme.font(.callout))
        .foregroundStyle(value.isEmpty ? theme.textDim : theme.text)
        .textSelection(.enabled)
        .frame(maxWidth: .infinity, minHeight: 58, alignment: .leading)
        .padding(.horizontal, 16)
        .background(theme.background)
        .overlay {
          RoundedRectangle(cornerRadius: 12)
            .stroke(theme.border, lineWidth: 1)
        }
        .accessibilityLabel(label)
        .accessibilityValue(value.isEmpty ? "Not scanned" : value)
    }
  }
}

struct GhostErrorBanner: View {
  @Environment(GhostTheme.self) private var theme

  let message: String

  var body: some View {
    Text("! \(message)")
      .font(theme.font(.footnote))
      .foregroundStyle(theme.warning)
      .frame(maxWidth: .infinity, alignment: .leading)
      .padding(12)
      .overlay {
        Rectangle().stroke(theme.warning, lineWidth: 1)
      }
  }
}

struct GhostMark: View {
  @Environment(GhostTheme.self) private var theme

  var body: some View {
    ZStack(alignment: .top) {
      GhostBody()
        .fill(theme.accent)

      HStack(spacing: 12) {
        Circle()
        Circle()
      }
      .foregroundStyle(theme.background)
      .frame(width: 30, height: 8)
      .padding(.top, 15)
    }
    .frame(width: 62, height: 72)
    .accessibilityHidden(true)
  }
}

private struct GhostBody: Shape {
  func path(in rect: CGRect) -> Path {
    var path = Path()
    let top = rect.height * 0.12
    let shoulder = rect.height * 0.37
    let bottom = rect.height * 0.94
    path.move(to: CGPoint(x: 0, y: bottom))
    path.addLine(to: CGPoint(x: 0, y: shoulder))
    path.addCurve(
      to: CGPoint(x: rect.width, y: shoulder),
      control1: CGPoint(x: 0, y: top),
      control2: CGPoint(x: rect.width, y: top)
    )
    path.addLine(to: CGPoint(x: rect.width, y: bottom))
    path.addLine(to: CGPoint(x: rect.width * 0.75, y: rect.height * 0.78))
    path.addLine(to: CGPoint(x: rect.width * 0.5, y: bottom))
    path.addLine(to: CGPoint(x: rect.width * 0.25, y: rect.height * 0.78))
    path.closeSubpath()
    return path
  }
}
