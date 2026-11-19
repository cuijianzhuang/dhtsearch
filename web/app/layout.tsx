import type { Metadata } from "next";
import Script from "next/script";
import type { CSSProperties } from "react";
import "./globals.css";
import { fetchSiteConfig } from "@/lib/site";
import { themePreset } from "@/lib/theme";

const DESCRIPTION =
  "干净、无广告的磁力链接搜索引擎。基于 DHT 网络实时收录，自动过滤成人内容与垃圾信息。";

export async function generateMetadata(): Promise<Metadata> {
  const site = await fetchSiteConfig();
  const title = `${site.name} - 干净的磁力链接搜索`;
  return {
    title: {
      default: title,
      template: `%s - ${site.name}`,
    },
    description: DESCRIPTION,
    openGraph: {
      title,
      description: DESCRIPTION,
      siteName: site.name,
      type: "website",
    },
  };
}

const gaId = process.env.NEXT_PUBLIC_GA_ID;

// Applies the visitor's stored appearance before first paint. The
// server renders data-appearance from the admin-set default; this
// runs earlier than any body content, so a stored choice that
// differs never flashes the wrong palette.
const THEME_BOOT_SCRIPT = `try{var a=localStorage.getItem("dhtsearch-appearance");if(a==="light"||a==="dark"||a==="system"){document.documentElement.dataset.appearance=a;}}catch(e){}`;

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const site = await fetchSiteConfig();
  const preset = themePreset(site.themeColor);
  const themeVars = {
    "--pri-base": preset.pri,
    "--pri-hover-base": preset.hover,
    "--pri-ink-light": preset.inkLight,
    "--pri-ink-dark": preset.inkDark,
  } as CSSProperties;
  return (
    <html
      lang="zh-CN"
      className="h-full"
      data-appearance={site.themeAppearance}
      data-theme-color={preset.key}
      style={themeVars}
      suppressHydrationWarning
    >
      <body className="flex min-h-full flex-col bg-canvas text-ink antialiased">
        <Script id="theme-boot" strategy="beforeInteractive">
          {THEME_BOOT_SCRIPT}
        </Script>
        {children}
        {gaId && (
          <>
            <Script
              src={`https://www.googletagmanager.com/gtag/js?id=${gaId}`}
              strategy="afterInteractive"
            />
            <Script id="gtag-init" strategy="afterInteractive">
              {`window.dataLayer = window.dataLayer || [];
                function gtag(){dataLayer.push(arguments);}
                gtag('js', new Date());
                gtag('config', '${gaId}');`}
            </Script>
          </>
        )}
      </body>
    </html>
  );
}
