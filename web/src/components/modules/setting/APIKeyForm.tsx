'use client';

import { useCallback, type FormEvent } from 'react';
import { Check, Loader, X } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { type APIKey } from '@/api/endpoints/apikey';
import {
    APIKeyBasicSection,
    APIKeyLimitSection,
    APIKeyModelAccessSection,
} from './APIKeyFormSections';
import { useAPIKeyFormState, type APIKeyFormValues } from './useAPIKeyFormState';

interface APIKeyFormProps {
    apiKey?: APIKey;
    isPending: boolean;
    submitLabel: string;
    onSubmit: (data: APIKeyFormValues) => void;
    onClose: () => void;
}

export function APIKeyForm({ apiKey, isPending, submitLabel, onSubmit, onClose }: APIKeyFormProps) {
    const t = useTranslations('setting.apiKey.form');
    const state = useAPIKeyFormState(apiKey);
    const handleSubmit = useCallback((event: FormEvent) => {
        event.preventDefault();
        if (state.form.name.trim()) onSubmit(state.form);
    }, [onSubmit, state.form]);

    return (
        <form onSubmit={handleSubmit} className="grid gap-3">
            <APIKeyBasicSection form={state.form} disabled={isPending} onChange={state.updateForm} />
            <APIKeyLimitSection
                disabled={isPending}
                maxCostInput={state.maxCostInput}
                isUnlimitedCost={state.isUnlimitedCost}
                expireOpen={state.expireOpen}
                expireDate={state.expireDate}
                expireTime={state.expireTime}
                neverExpire={state.neverExpire}
                onMaxCostChange={state.handleMaxCostChange}
                onClearMaxCost={state.handleClearMaxCost}
                onExpireOpenChange={state.setExpireOpen}
                onSelectDate={state.handleSelectDate}
                onExpireTimeChange={state.handleExpireTimeChange}
                onTimeBlur={state.handleTimeBlur}
                onToggleNeverExpire={state.handleToggleNeverExpire}
            />
            <APIKeyModelAccessSection
                supportedModels={state.form.supported_models}
                availableModels={state.availableModels}
                disabled={isPending}
                onToggleModel={state.handleToggleModel}
            />
            <div className="mt-3 flex gap-2 pt-2">
                <button type="button" onClick={onClose} disabled={isPending} className="flex h-9 flex-1 items-center justify-center gap-1.5 rounded-xl bg-muted text-sm font-medium text-muted-foreground transition-all hover:bg-muted/80 active:scale-[0.98] disabled:opacity-50">
                    <X className="size-4" />
                    {t('cancel')}
                </button>
                <button type="submit" disabled={isPending || !state.form.name.trim()} className="flex h-9 flex-1 items-center justify-center gap-1.5 rounded-xl bg-primary text-sm font-medium text-primary-foreground transition-all hover:bg-primary/90 active:scale-[0.98] disabled:opacity-50">
                    {isPending ? <Loader className="size-4 animate-spin" /> : <Check className="size-4" />}
                    {submitLabel}
                </button>
            </div>
        </form>
    );
}
