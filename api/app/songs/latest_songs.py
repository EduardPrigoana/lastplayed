import os
import requests
from flask import jsonify, request

BASE_URL = 'https://ws.audioscrobbler.com/2.0/'
RECENT_TRACKS_PARAMS = 'method=user.getrecenttracks&limit=1&format=json'
TIMEOUT = 10

def route(user):
    api_key = os.environ.get('LASTFM_API_KEY')
    if not api_key:
        return jsonify({"message": "INTERNAL_ERROR"}), 500
    try:
        req = requests.get(
            f"{BASE_URL}?{RECENT_TRACKS_PARAMS}&user={user}&api_key={api_key}",
            timeout=TIMEOUT
        )
        lastfm_response = req.json()
        recent_tracks = lastfm_response.get('recenttracks', {})
        if 'track' not in recent_tracks or not recent_tracks['track']:
            return jsonify({'message': 'NO_TRACKS_FOUND'}), 200

        track = recent_tracks['track'][0]

        # Add date parameter if available
        track_date = track.get('date', {}).get('uts')
        track['date_uts'] = int(track_date) if track_date else None

        if request.args.get('format') == 'shields.io':
            return jsonify({
                'schemaVersion': 1,
                'label': 'Last.FM Last Played Song',
                'message': f"{track['name']} - {track['artist']['#text']}"
            }), 200

        return jsonify({'track': track}), req.status_code
    except Exception:
        return jsonify({'message': 'INTERNAL_ERROR'}), 500
