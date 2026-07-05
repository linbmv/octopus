'use client';

import { type FormEvent } from 'react';
import { HelpCircle } from 'lucide-react';
import { useTranslations } from 'next-intl';
import { Button } from '@/components/ui/button';
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';
import { MODE_LABELS } from './utils';
import { GroupPickerSection } from './GroupPickerSection';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/animate-ui/components/animate/tooltip';
import { ModelPickerSection } from './ModelPickerSection';
import { MemberSortSection } from './MemberSortSection';
import { useGroupEditorState, type GroupEditorValues } from './useGroupEditorState';

export type { GroupEditorValues } from './useGroupEditorState';

export function GroupEditor({
    initial,
    submitText,
    submittingText,
    isSubmitting,
    onSubmit,
    onCancel,
}: {
    initial?: Partial<GroupEditorValues> & { id?: number };
    submitText: string;
    submittingText: string;
    isSubmitting: boolean;
    onSubmit: (values: GroupEditorValues) => void;
    onCancel?: () => void;
}) {
    const t = useTranslations('group');
    const editor = useGroupEditorState(initial);

    const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
        event.preventDefault();
        if (!editor.isValid) return;
        onSubmit(editor.values);
    };


    return (
        <form onSubmit={handleSubmit} className="flex flex-col h-full min-h-0 ">
            <div className="flex-1 min-h-0 overflow-hidden pr-1">
                <FieldGroup className="gap-4 flex flex-col min-h-0 h-full">
                    <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
                        <Field>
                            <FieldLabel htmlFor="group-name">{t('form.name')}</FieldLabel>
                            <Input
                                id="group-name"
                                value={editor.groupName}
                                onChange={(e) => editor.setGroupName(e.target.value)}
                                className="rounded-xl"
                            />
                        </Field>
                        <Field>
                            <FieldLabel htmlFor="group-match-regex">{t('form.matchRegex')}</FieldLabel>
                            <Input
                                id="group-match-regex"
                                value={editor.matchRegex}
                                onChange={(e) => editor.setMatchRegex(e.target.value)}
                                className="rounded-xl"
                                placeholder={t('form.matchRegexPlaceholder')}
                            />
                            {editor.regexError && (
                                <p className="mt-1 text-xs text-destructive">
                                    {t('form.matchRegexInvalid')}: {editor.regexError}
                                </p>
                            )}
                        </Field>

                        <Field>
                            <FieldLabel htmlFor="group-first-token-time-out">
                                {t('form.firstTokenTimeOut')}
                                <TooltipProvider>
                                    <Tooltip>
                                        <TooltipTrigger asChild>
                                            <HelpCircle className="size-4 text-muted-foreground cursor-help" />
                                        </TooltipTrigger>
                                        <TooltipContent>
                                            {t('form.firstTokenTimeOutHint')}
                                        </TooltipContent>
                                    </Tooltip>
                                </TooltipProvider>
                            </FieldLabel>
                            <Input
                                id="group-first-token-time-out"
                                type="number"
                                inputMode="numeric"
                                min={0}
                                step={1}
                                value={String(editor.firstTokenTimeOut)}
                                onChange={(e) => editor.handleFirstTokenTimeOutChange(e.target.value)}
                                className="rounded-xl"
                            />
                        </Field>

                        <Field>
                            <FieldLabel htmlFor="group-session-keep-time">
                                {t('form.sessionKeepTime')}
                                <TooltipProvider>
                                    <Tooltip>
                                        <TooltipTrigger asChild>
                                            <HelpCircle className="size-4 text-muted-foreground cursor-help" />
                                        </TooltipTrigger>
                                        <TooltipContent>
                                            {t('form.sessionKeepTimeHint')}
                                        </TooltipContent>
                                    </Tooltip>
                                </TooltipProvider>
                            </FieldLabel>
                            <Input
                                id="group-session-keep-time"
                                type="number"
                                inputMode="numeric"
                                min={0}
                                step={1}
                                value={String(editor.sessionKeepTime)}
                                onChange={(e) => editor.handleSessionKeepTimeChange(e.target.value)}
                                className="rounded-xl"
                            />
                        </Field>
                    </div>

                    {/* Mode */}
                    <div className="flex gap-1">
                        {([1, 2, 3, 4] as const).map((m) => (
                            <button
                                key={m}
                                type="button"
                                onClick={() => editor.setMode(m)}
                                className={cn(
                                    'flex-1 py-1 text-xs rounded-lg transition-colors',
                                    editor.mode === m ? 'bg-primary text-primary-foreground' : 'bg-muted hover:bg-muted/80'
                                )}
                            >
                                {t(`mode.${MODE_LABELS[m]}`)}
                            </button>
                        ))}
                    </div>

                    <div className="flex-1 min-h-0">
                        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 h-full min-h-0">
                            <ModelPickerSection
                                modelChannels={editor.modelChannels}
                                selectedMembers={editor.selectedMembers}
                                onAdd={editor.handleAddMember}
                                onAutoAdd={editor.handleAutoAdd}
                                autoAddDisabled={editor.autoAddDisabled}
                            />
                            <GroupPickerSection
                                groups={editor.allGroups}
                                selectedMembers={editor.selectedMembers}
                                currentGroupId={editor.currentGroupId}
                                onAdd={editor.handleAddGroup}
                            />
                            <MemberSortSection
                                members={editor.selectedMembers}
                                onReorder={editor.setSelectedMembers}
                                onRemove={editor.handleRemoveMember}
                                onWeightChange={editor.handleWeightChange}
                                removingIds={editor.removingIds}
                                showWeight={editor.mode === 4}
                                onClear={editor.handleClearMembers}
                            />
                        </div>
                    </div>
                </FieldGroup>
            </div>

            <div className="pt-4 mt-auto shrink-0">
                <div className="flex gap-2">
                    {onCancel && (
                        <Button type="button" variant="secondary" className="flex-1 rounded-xl h-11" onClick={onCancel}>
                            {t('detail.actions.cancel')}
                        </Button>
                    )}
                    <Button
                        type="submit"
                        disabled={!editor.isValid || isSubmitting}
                        className="flex-1 rounded-xl h-11"
                    >
                        {isSubmitting ? submittingText : submitText}
                    </Button>
                </div>
            </div>
        </form>
    );
}
