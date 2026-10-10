/** Grey placeholder bars while something loads. */
export function SkeletonLine({ width = '100%', height = 12 }: { width?: string; height?: number }) {
    return <span className="skeleton" style={{ width, height }} aria-hidden="true" />;
}

/** Placeholder cards for a section whose list is loading. */
export function SkeletonCards({ count }: { count: number }) {
    return (
        <div className="cards" aria-busy="true" aria-label="Loading">
            {Array.from({ length: count }, (_, i) => (
                <div key={i} className="card card-skeleton">
                    <SkeletonLine width={i % 2 ? '28%' : '36%'} height={14} />
                    <SkeletonLine width={i % 2 ? '62%' : '48%'} height={10} />
                </div>
            ))}
        </div>
    );
}
