'use client';

import { useEffect } from 'react';
import { AlertTriangle, RefreshCw } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { Button } from '@/components/ui/button';
import { logger } from '@/lib/logger';

export default function Error({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  const t = useTranslations('appError');

  useEffect(() => {
    logger.error('Unhandled application error', error);
  }, [error]);

  return (
    <main className="flex min-h-screen items-center justify-center bg-background px-4 py-10 text-foreground">
      <section className="w-full max-w-md rounded-lg border border-border bg-card p-6 shadow-sm">
        <div className="flex items-center gap-3">
          <div className="flex size-10 items-center justify-center rounded-md bg-destructive/10 text-destructive">
            <AlertTriangle className="size-5" />
          </div>
          <div className="min-w-0">
            <h1 className="text-base font-semibold text-card-foreground">{t('pageTitle')}</h1>
            <p className="mt-1 text-sm text-muted-foreground">{t('description')}</p>
          </div>
        </div>

        {error.digest ? (
          <p className="mt-4 break-all rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
            {t('digest')}: {error.digest}
          </p>
        ) : null}

        <div className="mt-6 flex justify-end">
          <Button onClick={reset} size="sm">
            <RefreshCw className="size-4" />
            {t('retry')}
          </Button>
        </div>
      </section>
    </main>
  );
}
