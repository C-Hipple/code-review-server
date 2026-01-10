# Design System

A lightweight, custom design system for the Code Review application built with pure React and TypeScript. No external dependencies required.

## Architecture

```
design/
├── components/      # Reusable UI components
├── styles/          # Design tokens and utilities
└── index.ts         # Main export
```

## Components

### Button

Versatile button component with multiple variants and states.

**Props:**
- `variant`: `'primary' | 'secondary' | 'ghost' | 'danger'` (default: `'primary'`)
- `size`: `'sm' | 'md' | 'lg'` (default: `'md'`)
- `loading`: `boolean` - Shows spinner when true
- `icon`: `React.ReactNode` - Optional icon
- `fullWidth`: `boolean` - Makes button full width

**Examples:**
```tsx
import { Button } from '../design';

<Button>Click me</Button>
<Button variant="secondary">Cancel</Button>
<Button variant="ghost">Plugins</Button>
<Button loading={isLoading}>Save</Button>
<Button size="sm">Small</Button>
```

### Badge

Status indicator component for labels and tags.

**Props:**
- `variant`: `'success' | 'danger' | 'warning' | 'info' | 'neutral'` (default: `'neutral'`)
- `size`: `'sm' | 'md' | 'lg'` (default: `'md'`)
- `pill`: `boolean` - Fully rounded shape
- `icon`: `React.ReactNode` - Optional icon

**Examples:**
```tsx
import { Badge, mapStatusToVariant } from '../design';

<Badge variant="success">Done</Badge>
<Badge variant="danger">Error</Badge>
<Badge variant="warning" size="sm">TODO</Badge>
<Badge variant={mapStatusToVariant(status)}>{status}</Badge>
```

### Card

Container component with hover effects and variants.

**Props:**
- `variant`: `'default' | 'elevated' | 'outlined'` (default: `'default'`)
- `padding`: `'none' | 'sm' | 'md' | 'lg'` (default: `'lg'`)
- `hover`: `boolean` - Enable hover effects
- `gradient`: `boolean` - Gradient background
- `onClick`: `() => void` - Click handler

**Examples:**
```tsx
import { Card } from '../design';

<Card>Default card</Card>
<Card variant="outlined" hover padding="md">
  Interactive card
</Card>
<Card variant="elevated" gradient>
  Fancy card
</Card>
```

### Input & TextArea

Form input components with labels and error states.

**Props:**
- `label`: `string` - Optional label
- `error`: `string` - Error message
- `icon`: `React.ReactNode` - Leading icon (Input only)
- `clearable`: `boolean` - Show clear button (Input only)
- `onClear`: `() => void` - Clear handler (Input only)

**Examples:**
```tsx
import { Input, TextArea } from '../design';

<Input
  label="Username"
  placeholder="Enter username"
  value={username}
  onChange={e => setUsername(e.target.value)}
/>

<Input
  icon={<span>⌕</span>}
  clearable
  onClear={() => setSearch('')}
  placeholder="Search..."
/>

<TextArea
  label="Comment"
  placeholder="Write a comment..."
  rows={5}
/>
```

### Select

Dropdown select component with consistent styling.

**Props:**
- `label`: `string` - Optional label
- `options`: `Array<{ value: string; label: string }>` - Dropdown options

**Examples:**
```tsx
import { Select } from '../design';

<Select
  label="Status"
  value={status}
  onChange={e => setStatus(e.target.value)}
  options={[
    { value: 'todo', label: 'TODO' },
    { value: 'done', label: 'Done' },
  ]}
/>
```

### Modal

Modal dialog with backdrop and ESC key handling.

**Props:**
- `isOpen`: `boolean` - Open state
- `onClose`: `() => void` - Close handler
- `title`: `string` - Modal title
- `footer`: `React.ReactNode` - Optional footer
- `size`: `'sm' | 'md' | 'lg' | 'xl'` (default: `'md'`)

**Examples:**
```tsx
import { Modal, Button } from '../design';

<Modal
  isOpen={showModal}
  onClose={() => setShowModal(false)}
  title="Confirm Action"
  size="sm"
>
  <p>Are you sure you want to proceed?</p>
  <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end', marginTop: '16px' }}>
    <Button onClick={() => setShowModal(false)} variant="secondary">Cancel</Button>
    <Button onClick={handleConfirm}>Confirm</Button>
  </div>
</Modal>
```

## Design Tokens

### Colors

```typescript
import { colors } from '../design';

colors.bgPrimary      // #0f1115
colors.bgSecondary    // #161b22
colors.bgTertiary     // #21262d
colors.textPrimary    // #f0f6fc
colors.textSecondary  // #8b949e
colors.accent         // #58a6ff
colors.border         // #30363d
colors.success        // #238636
colors.danger         // #da3633
colors.warning        // #d29922
```

### Spacing

```typescript
import { spacing } from '../design';

spacing.xs   // 4px
spacing.sm   // 8px
spacing.md   // 12px
spacing.lg   // 16px
spacing.xl   // 20px
spacing['2xl'] // 24px
spacing['3xl'] // 32px
```

### Border Radius

```typescript
import { borderRadius } from '../design';

borderRadius.sm   // 4px
borderRadius.md   // 6px
borderRadius.lg   // 8px
borderRadius.pill // 999px
```

### Typography

```typescript
import { fontSize, fontWeight } from '../design';

fontSize.xs    // 10px
fontSize.sm    // 12px
fontSize.base  // 13px
fontSize.md    // 14px
// ... more sizes

fontWeight.normal    // 400
fontWeight.medium    // 500
fontWeight.semibold  // 600
fontWeight.bold      // 700
```

## Utilities

### Status Mapping

```typescript
import { mapStatusToVariant } from '../design';

// Maps status strings to badge variants
const variant = mapStatusToVariant('success'); // 'success'
const variant = mapStatusToVariant('error');   // 'danger'
const variant = mapStatusToVariant('pending'); // 'warning'
```

## Usage Pattern

```tsx
// Import what you need
import { Button, Badge, Card, Input, Select, Modal } from '../design';
import { colors, spacing, mapStatusToVariant } from '../design';

function MyComponent() {
  return (
    <Card padding="lg">
      <div style={{ display: 'flex', gap: spacing.md, alignItems: 'center' }}>
        <Badge variant={mapStatusToVariant(status)}>{status}</Badge>
        <h2 style={{ color: colors.textPrimary }}>Title</h2>
      </div>

      <Input label="Name" placeholder="Enter name" />

      <div style={{ display: 'flex', gap: spacing.md, marginTop: spacing.lg }}>
        <Button variant="secondary">Cancel</Button>
        <Button>Submit</Button>
      </div>
    </Card>
  );
}
```

## Design Principles

1. **No Dependencies** - Pure React implementation, no external UI libraries
2. **TypeScript First** - Full type safety for all components
3. **Consistent Styling** - All components use design tokens
4. **Accessible** - Focus states, keyboard navigation, ARIA support
5. **Flexible** - Props for customization without breaking consistency
6. **Performant** - No runtime CSS generation, minimal re-renders

## Migration Notes

### Before (Inline Styles)
```tsx
<button style={{
  background: 'var(--accent)',
  color: 'white',
  border: 'none',
  padding: '8px 16px',
  borderRadius: '6px',
  fontSize: '14px',
  fontWeight: 500,
  cursor: 'pointer',
}}>
  Click me
</button>
```

### After (Design System)
```tsx
<Button>Click me</Button>
```

## Browser Support

- Chrome/Edge (latest)
- Firefox (latest)
- Safari (latest)

## Contributing

When adding new components:
1. Create component in `design/components/`
2. Export from `design/components/index.ts`
3. Add documentation to this README
4. Use existing design tokens
5. Ensure TypeScript types are complete
