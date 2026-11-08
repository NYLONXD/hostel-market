// Shapes returned by the FastAPI backend (see backend/app/routers).

export type Role = "student" | "admin";
export type ListingStatus = "active" | "reserved" | "sold" | "removed";
export type Condition = "new" | "like_new" | "good" | "fair" | "poor";
export type OrderStatus =
  | "requested"
  | "accepted"
  | "awaiting_payment"
  | "paid"
  | "handed_over"
  | "completed"
  | "rejected"
  | "cancelled"
  | "expired";
export type OrderAction =
  | "accept"
  | "reject"
  | "cancel"
  | "choose_payment"
  | "confirm_payment"
  | "handover"
  | "complete";

export interface User {
  user_id: number;
  email: string;
  full_name: string;
  role: Role;
  is_verified: boolean;
  verified_at: string | null;
  year_of_study: number;
  upi_id: string | null;
  block_id: number;
  block_name: string;
  hostel_id: number;
  hostel_name: string;
  created_at: string;
}

export interface SellerStats {
  active_listings: number;
  reserved_listings: number;
  sold_listings: number;
  total_earned: number;
}

export interface Session {
  access_token: string;
  expires_at: string;
  user: User;
}

export interface Hostel {
  hostel_id: number;
  name: string;
  blocks: { block_id: number; name: string }[];
}

export interface Category {
  category_id: number;
  slug: string;
  name: string;
}

export interface Listing {
  listing_id: number;
  title: string;
  description: string;
  price: number;
  condition: Condition;
  status: ListingStatus;
  category: string;
  category_name: string;
  seller_id: number;
  seller_name: string;
  seller_block: string;
  hostel_id: number;
  hostel_name: string;
  available_from: string | null;
  available_until: string | null;
  allowed_block_id: number | null;
  allowed_block: string | null;
  allowed_year: number | null;
  cover_url: string | null;
  photo_count: number;
  created_at: string;
  updated_at: string;
  pending_requests?: number;
}

export interface Photo {
  photo_id: number;
  url: string;
  position: number;
}

export interface ListingDetail extends Listing {
  photos: Photo[];
  viewer: {
    is_seller: boolean;
    pending_requests?: number;
    can_buy?: boolean;
    block_reason?: string | null;
    block_message?: string | null;
    my_order?: { order_id: number; status: OrderStatus } | null;
    conversation_id?: number | null;
  };
}

export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  limit: number;
}

export interface OrderHistory {
  history_id: number;
  old_status: OrderStatus | null;
  new_status: OrderStatus;
  changed_by: number | null;
  changed_by_name: string | null;
  note: string | null;
  changed_at: string;
}

export interface Order {
  order_id: number;
  listing_id: number;
  listing_title: string;
  listing_status: ListingStatus;
  listing_cover_url: string | null;
  buyer_id: number;
  buyer_name: string;
  seller_id: number;
  seller_name: string;
  seller_upi_id: string | null;
  price: number;
  status: OrderStatus;
  payment_method: "upi" | "cash" | null;
  buyer_note: string | null;
  request_expires_at: string;
  accepted_at: string | null;
  paid_at: string | null;
  handed_over_at: string | null;
  completed_at: string | null;
  closed_at: string | null;
  close_reason: string | null;
  created_at: string;
  my_role: "buyer" | "seller" | "admin";
  history?: OrderHistory[];
}

export interface Conversation {
  conversation_id: number;
  listing_id: number;
  listing_title: string;
  listing_status: ListingStatus;
  listing_cover_url: string | null;
  buyer_id: number;
  buyer_name: string;
  seller_id: number;
  seller_name: string;
  other_name: string;
  my_last_read_id: number;
  other_last_read_id: number;
  unread: number;
  last_message: string | null;
  last_message_at: string | null;
  created_at: string;
}

export interface Message {
  message_id: number;
  sender_id: number;
  body: string;
  created_at: string;
}

export interface AdminStats {
  totals: {
    users: number;
    unverified_users: number;
    active_listings: number;
    sold_listings: number;
    open_requests: number;
    completed_orders: number;
    total_sales_value: number;
  };
  orders_by_status: { status: OrderStatus; count: number }[];
  hostels: { hostel_id: number; hostel_name: string; residents: number; active_listings: number }[];
  categories: { category_name: string; active_listings: number; avg_price: number }[];
  top_sellers: { user_id: number; full_name: string; sold_listings: number; total_earned: number }[];
}
