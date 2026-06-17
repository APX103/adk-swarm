import { OpenAI } from 'openai';

export const openai = new OpenAI({
  apiKey: process.env.OPENAI_API_KEY || '',
  baseURL: process.env.OPENAI_BASE_URL || '',
});

export const MODEL = process.env.OPENAI_MODEL || 'glm-4.5-air';
