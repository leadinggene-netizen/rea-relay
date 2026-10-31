import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "rea-relay — TradingView → MT5 bridge",
  description:
    "Hosted TradingView → MT4/MT5 alert bridge. Server-side risk, multi-account fan-out, priced to undercut the incumbent.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body className="min-h-screen antialiased">{children}</body>
    </html>
  );
}
