// Greeting helpers used by the CLI entry point.

export interface Greeting {
    name: string;
    punctuation: string;
}

export function formatGreeting(greeting: Greeting): string {
    return `Hello, ${greeting.name}${greeting.punctuation}`;
}
