// Mock switch. A public variable injected at build time (only NEXT_PUBLIC_ variables are readable in the browser).
// Set NEXT_PUBLIC_MOCK=1 on Vercel to run the whole site on mocks without a backend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
