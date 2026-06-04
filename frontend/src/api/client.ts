import { useAccessStore } from 'shell/vben/stores';

const MODULE_BASE_URL = '/admin/v1/modules/ticket/v1';

function authHeaders(): Record<string, string> {
  const accessStore = useAccessStore();
  const token = (accessStore as any).accessToken;
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(`${MODULE_BASE_URL}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...authHeaders(),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    let message = `HTTP error! status: ${response.status}`;
    try {
      const err = await response.json();
      if (err?.message) message = err.message;
    } catch {
      // ignore non-JSON error bodies
    }
    throw new Error(message);
  }
  const text = await response.text();
  return (text ? JSON.parse(text) : {}) as T;
}

export const ticketApi = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  delete: <T>(path: string) => request<T>('DELETE', path),
};

export default ticketApi;
