import { createContext, useContext } from 'react'
import type { FeedSnapshot } from '@/api/sse'

export const EventsContext = createContext<FeedSnapshot>({ status: 'connecting', events: [] })
export const useEventFeed = () => useContext(EventsContext)
