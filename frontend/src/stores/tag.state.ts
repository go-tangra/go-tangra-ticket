import { defineStore } from 'pinia';

import {
  ruleService,
  tagService,
  type RuleInput,
  type TicketRule,
  type TicketTag,
} from '../api/client';

export const useTagStore = defineStore('ticket-tag', () => {
  // --- Tags / categories ---

  async function listTags(kind?: string): Promise<TicketTag[]> {
    return (await tagService.ListTags(kind ? { kind } : {})).tags ?? [];
  }

  async function createTag(input: {
    name: string;
    kind: string;
    color: string;
    description: string;
  }): Promise<TicketTag | undefined> {
    return (await tagService.CreateTag(input)).tag;
  }

  async function updateTag(
    id: string,
    patch: { name?: string; color?: string; description?: string },
  ): Promise<TicketTag | undefined> {
    return (await tagService.UpdateTag({ id, ...patch })).tag;
  }

  async function deleteTag(id: string): Promise<void> {
    await tagService.DeleteTag({ id });
  }

  async function setTicketTags(
    ticketId: string,
    tagIds: string[],
  ): Promise<void> {
    await tagService.SetTicketTags({ ticketId, tagIds });
  }

  // --- Auto-tagging rules ---

  async function listRules(): Promise<TicketRule[]> {
    return (await ruleService.ListRules({})).rules ?? [];
  }

  async function createRule(rule: RuleInput): Promise<TicketRule | undefined> {
    return (await ruleService.CreateRule({ rule })).rule;
  }

  async function updateRule(
    id: string,
    rule: RuleInput,
  ): Promise<TicketRule | undefined> {
    return (await ruleService.UpdateRule({ id, rule })).rule;
  }

  async function deleteRule(id: string): Promise<void> {
    await ruleService.DeleteRule({ id });
  }

  function $reset() {}

  return {
    $reset,
    listTags,
    createTag,
    updateTag,
    deleteTag,
    setTicketTags,
    listRules,
    createRule,
    updateRule,
    deleteRule,
  };
});
