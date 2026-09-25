import { formatGreeting } from './greet';

const message = formatGreeting({ name: 'world', punctuation: '!' });
console.log(message);
