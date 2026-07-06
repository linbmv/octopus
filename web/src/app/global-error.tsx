'use client';

import './globals.css';
import { useEffect } from 'react';
import { logger } from '@/lib/logger';

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    logger.error('Unhandled root application error', error);
  }, [error]);

  return (
    <html suppressHydrationWarning>
      <body className="min-h-screen bg-background text-foreground antialiased">
        <main className="flex min-h-screen items-center justify-center px-4 py-10">
          <section className="w-full max-w-md rounded-lg border border-border bg-card p-6 shadow-sm">
            <div className="flex items-start gap-3">
              <div className="flex size-10 shrink-0 items-center justify-center rounded-md bg-destructive/10 text-destructive">
                <span className="text-lg font-semibold">!</span>
              </div>
              <div className="min-w-0">
                <h1 className="text-base font-semibold text-card-foreground">应用加载失败</h1>
                <p className="mt-1 text-sm text-muted-foreground">请重试，或刷新页面后继续操作。</p>
              </div>
            </div>

            {error.digest ? (
              <p className="mt-4 break-all rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
                错误编号：{error.digest}
              </p>
            ) : null}

            <div className="mt-6 flex justify-end">
              <button
                type="button"
                onClick={reset}
                className="inline-flex h-8 items-center justify-center rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              >
                重试
              </button>
            </div>
          </section>
        </main>
      </body>
    </html>
  );
}
