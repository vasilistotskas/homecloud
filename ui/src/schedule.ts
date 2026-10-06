/**
 * When a job that runs after sync cycles runs next. Jobs run at the first
 * cycle at or after they are due; cycles run every `cycleSeconds` from
 * `nextCycle`. A job that never finished (`last` undefined) runs at the next
 * cycle.
 */
export function nextRunAt(
  last: Date | undefined,
  everySeconds: bigint,
  nextCycle: Date | undefined,
  cycleSeconds: bigint,
): Date | undefined {
  if (nextCycle === undefined) {
    return undefined;
  }
  if (last === undefined) {
    return nextCycle;
  }
  const due = last.getTime() + Number(everySeconds) * 1000;
  if (due <= nextCycle.getTime()) {
    return nextCycle;
  }
  const cycle = Number(cycleSeconds) * 1000;
  return new Date(nextCycle.getTime() + Math.ceil((due - nextCycle.getTime()) / cycle) * cycle);
}
