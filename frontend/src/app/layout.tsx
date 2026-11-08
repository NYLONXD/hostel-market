import type { Metadata } from "next";
import { Bricolage_Grotesque, Inter } from "next/font/google";
import { Suspense } from "react";

import { AuthProvider } from "@/components/AuthProvider";
import { Nav } from "@/components/Nav";
import { Spinner } from "@/components/ui";

import "./globals.css";

const heading = Bricolage_Grotesque({ variable: "--font-heading", subsets: ["latin"] });
const body = Inter({ variable: "--font-body", subsets: ["latin"] });

export const metadata: Metadata = {
  title: "Hostel Market",
  description: "Buy and sell used books, cycles and appliances inside your hostel.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" className={`${heading.variable} ${body.variable} h-full antialiased`}>
      <body className="flex min-h-full flex-col font-sans">
        <AuthProvider>
          {/* Nav and pages read the URL (usePathname/useParams), which Next.js 16 streams in
              behind Suspense so the static shell can be prerendered. */}
          <Suspense fallback={<div className="h-[61px] border-b border-line" />}>
            <Nav />
          </Suspense>
          <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:py-8">
            <Suspense fallback={<Spinner />}>{children}</Suspense>
          </main>
          <footer className="border-t border-line py-6 text-center text-xs text-muted">
            Hostel Market · DBMS mini project · FastAPI + PostgreSQL + Next.js
          </footer>
        </AuthProvider>
      </body>
    </html>
  );
}
