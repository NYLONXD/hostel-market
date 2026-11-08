# Hostel Market: web app

Next.js 16 (App Router) + TypeScript + Tailwind CSS 4. Every page is a client component
that talks to the FastAPI backend in `../backend`.

```powershell
npm install
Copy-Item .env.example .env.local    # NEXT_PUBLIC_API_URL=http://localhost:8000/api
npm run dev                          # http://localhost:3000
npm run build; npm run lint
```

| Path | Page |
|---|---|
| `/` | landing page (redirects to `/listings` when signed in) |
| `/login`, `/register` | sign in / sign up |
| `/listings` | hostel feed with search, filters, sorting, paging, recently viewed |
| `/listings/new`, `/listings/[id]`, `/listings/[id]/edit` | sell, view (buy / chat / manage requests), edit + photos |
| `/my-listings` | the seller's own listings by status |
| `/orders`, `/orders/[id]` | orders (buying / selling), order page with timeline and next actions |
| `/chat`, `/chat/[id]` | conversations; messages are polled every 4 s |
| `/profile` | profile, UPI ID, seller stats |
| `/admin` | statistics, verification, listing removal, hostels/blocks/categories |

Code: `src/lib/api.ts` (fetch wrapper), `src/lib/local.ts` (localStorage: session, filters,
draft, recently viewed), `src/components/` (auth context, nav, forms, UI pieces).
