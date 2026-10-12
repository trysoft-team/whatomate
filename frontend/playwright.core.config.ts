import { defineConfig } from '@playwright/test'
import base from './playwright.config'

// The core E2E set CI runs (under 200 tests): sign-in, chat, campaigns, users,
// roles, contacts, dashboard and chatbot keywords. `npm run test:e2e` still
// runs every spec.
export default defineConfig({
  ...base,
  testMatch: [
    'auth/login.spec.ts',
    'auth/register.spec.ts',
    'chat/chat.spec.ts',
    'chat/chat-composer.spec.ts',
    'chat/template-sending.spec.ts',
    'chat/message-bubbles.spec.ts',
    'chat/service-window.spec.ts',
    'campaigns/campaigns.spec.ts',
    'settings/users.spec.ts',
    'settings/roles.spec.ts',
    'settings/contacts.spec.ts',
    'dashboard/dashboard.spec.ts',
    'chatbot/keywords.spec.ts',
  ],
})
